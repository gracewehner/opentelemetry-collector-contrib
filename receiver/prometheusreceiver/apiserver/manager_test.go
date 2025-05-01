package apiserver // import "github.com/open-telemetry/opentelemetry-collector-contrib/receiver/prometheusreceiver/apiserver"

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/open-telemetry/opentelemetry-collector-contrib/receiver/prometheusreceiver/internal/metadata"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/common/model"
	"github.com/prometheus/prometheus/scrape"
	"github.com/stretchr/testify/require"
	"github.com/tj/assert"
	"go.opentelemetry.io/collector/component/componenttest"
	"go.opentelemetry.io/collector/config/confighttp"
	"go.opentelemetry.io/collector/receiver/receivertest"
)

func TestAPIServerManagerStart(t *testing.T) {
	for _, tc := range []struct {
		desc         string
		cfg          Config
		expectErr    bool
		errorMessage string
	}{
		{
			desc: "basic config",
			cfg: Config{
				ServerConfig: &confighttp.ServerConfig{
					Endpoint: "localhost:9090",
				},
			},
			expectErr: false,
		},
		{
			desc: "with CORS config",
			cfg: Config{
				ServerConfig: &confighttp.ServerConfig{
					Endpoint: "localhost:9091",
					CORS: &confighttp.CORSConfig{
						AllowedOrigins: []string{"https://example.com", "https://test.com"},
					},
				},
			},
			expectErr: false,
		},
		{
			desc: "with custom read timeout",
			cfg: &apiserver.Config{
				ServerConfig: &confighttp.ServerConfig{
					Endpoint:    "localhost:9092",
					ReadTimeout: 5 * time.Minute,
				},
			},
			expectErr: false,
		},
		{
			desc: "invalid CORS regex",
			cfg: &apiserver.Config{
				ServerConfig: &confighttp.ServerConfig{
					Endpoint: "localhost:9093",
					CORS: &confighttp.CORSConfig{
						AllowedOrigins: []string{"(invalid[regex"},
					},
				},
			},
			expectErr:    true,
			errorMessage: "failed to compile combined CORS allowed origins into regex",
		},
	} {
		t.Run(tc.desc, func(t *testing.T) {
			ctx := context.Background()

			registry := prometheus.NewRegistry()
			registerer := prometheus.WrapRegistererWithPrefix("prometheus_receiver_", registry)

			baseCfg := promconfig.Config{GlobalConfig: promconfig.DefaultGlobalConfig}
			scrapeManager := scrape.NewManager(&scrape.Options{}, nil, nil)

			manager := apiserver.NewManager(
				receivertest.NewNopSettings(metadata.Type),
				tc.cfg,
				baseCfg,
				scrapeManager,
				registry,
				registerer,
			)

			err := manager.Start(ctx, componenttest.NewNopHost())

			if tc.expectErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.errorMessage)
			} else {
				require.NoError(t, err)

				// Verify server is running by making a request to /api/v1/status/buildinfo
				client := &http.Client{
					Transport: &http.Transport{
						TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
					},
				}

				// Use a retry mechanism since the server might not be immediately available
				var resp *http.Response
				var requestErr error

				for i := 0; i < 3; i++ {
					url := fmt.Sprintf("http://%s/api/v1/status/buildinfo", tc.cfg.ServerConfig.Endpoint)
					resp, requestErr = client.Get(url)
					if requestErr == nil && resp.StatusCode == http.StatusOK {
						break
					}
					time.Sleep(100 * time.Millisecond)
				}

				require.NoError(t, requestErr)
				require.Equal(t, http.StatusOK, resp.StatusCode)
				resp.Body.Close()

				// Cleanup
				require.NoError(t, manager.Shutdown(ctx))
			}
		})
	}
}

func TestAPIServerManagerApplyConfig(t *testing.T) {
	ctx := context.Background()
	cfg := &apiserver.Config{
		ServerConfig: &confighttp.ServerConfig{
			Endpoint: "localhost:9094",
		},
	}

	registry := prometheus.NewRegistry()
	registerer := prometheus.WrapRegistererWithPrefix("prometheus_receiver_", registry)

	initialCfg := promconfig.Config{GlobalConfig: promconfig.DefaultGlobalConfig}
	scrapeManager := scrape.NewManager(&scrape.Options{}, nil, nil)

	manager := apiserver.NewManager(
		receivertest.NewNopSettings(metadata.Type),
		cfg,
		initialCfg,
		scrapeManager,
		registry,
		registerer,
	)

	require.NoError(t, manager.Start(ctx, componenttest.NewNopHost()))

	// Apply new config
	newCfg := promconfig.Config{
		GlobalConfig: promconfig.GlobalConfig{
			ScrapeInterval: model.Duration(30 * time.Second),
		},
	}
	manager.ApplyConfig(&newCfg)

	// Make a request to verify the config was applied
	client := &http.Client{}
	url := fmt.Sprintf("http://%s/api/v1/status/config", cfg.ServerConfig.Endpoint)

	var resp *http.Response
	var requestErr error

	for i := 0; i < 3; i++ {
		resp, requestErr = client.Get(url)
		if requestErr == nil && resp.StatusCode == http.StatusOK {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	require.NoError(t, requestErr)
	require.Equal(t, http.StatusOK, resp.StatusCode)

	// Parse the response to verify the config
	var result map[string]interface{}
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	resp.Body.Close()

	err = json.Unmarshal(body, &result)
	require.NoError(t, err)

	// Verify the config contains our updated scrape interval
	data, ok := result["data"].(map[string]interface{})
	require.True(t, ok)
	yamlConfig, ok := data["yaml"].(string)
	require.True(t, ok)
	assert.Contains(t, yamlConfig, "scrape_interval: 30s")

	// Cleanup
	require.NoError(t, manager.Shutdown(ctx))
}

func TestAPIServerManagerShutdown(t *testing.T) {
	ctx := context.Background()
	cfg := &apiserver.Config{
		ServerConfig: &confighttp.ServerConfig{
			Endpoint: "localhost:9095",
		},
	}

	registry := prometheus.NewRegistry()
	registerer := prometheus.WrapRegistererWithPrefix("prometheus_receiver_", registry)

	baseCfg := promconfig.Config{GlobalConfig: promconfig.DefaultGlobalConfig}
	scrapeManager := scrape.NewManager(&scrape.Options{}, nil, nil)

	manager := NewManager(
		receivertest.NewNopSettings(metadata.Type),
		cfg,
		baseCfg,
		scrapeManager,
		registry,
		registerer,
	)

	require.NoError(t, manager.Start(ctx, componenttest.NewNopHost()))

	// Verify server is running
	client := &http.Client{}
	url := fmt.Sprintf("http://%s/api/v1/status/buildinfo", cfg.ServerConfig.Endpoint)
	resp, err := client.Get(url)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	resp.Body.Close()

	// Shutdown the server
	require.NoError(t, manager.Shutdown(ctx))

	// Verify server is no longer running
	_, err = client.Get(url)
	require.Error(t, err)
}
