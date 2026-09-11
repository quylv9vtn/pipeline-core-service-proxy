// Package config loads gateway settings from the environment and the
// regions file (mounted from a ConfigMap in Kubernetes).
//
// Environment loading mirrors the KFP apiserver convention (viper
// AutomaticEnv): PORT, LOG_LEVEL, REGIONS_CONFIG_PATH, MAX_JSON_BODY_BYTES,
// HEALTH_CHECK_INTERVAL_SECONDS, SWAGGER_SOURCE_REGION,
// SWAGGER_FETCH_TIMEOUT_SECONDS.
package config

import (
	"fmt"
	"strings"
	"time"

	"github.com/spf13/viper"
)

const (
	DefaultPort              = 8080
	DefaultLogLevel          = "info"
	DefaultRegionsConfigPath = "/etc/core-gateway/regions.json"
	DefaultMaxJSONBodyBytes  = 32 << 20 // 32 MiB
	DefaultHealthCheckSec    = 30
	DefaultSwaggerFetchSec   = 10
)

// GatewayConfig is the static process configuration (env-driven).
type GatewayConfig struct {
	Port                   int
	LogLevel               string
	RegionsConfigPath      string
	MaxJSONBodyBytes       int64
	HealthCheckInterval    time.Duration // <= 0 disables the prober
	SwaggerSourceRegion    string        // empty = first region (sorted by name)
	SwaggerFetchTimeout    time.Duration
	SwaggerRefreshInterval time.Duration
}

// Load reads the process configuration from the environment with defaults.
func Load() (*GatewayConfig, error) {
	v := viper.New()
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	v.AutomaticEnv()

	v.SetDefault("port", DefaultPort)
	v.SetDefault("log_level", DefaultLogLevel)
	v.SetDefault("regions_config_path", DefaultRegionsConfigPath)
	v.SetDefault("max_json_body_bytes", DefaultMaxJSONBodyBytes)
	v.SetDefault("health_check_interval_seconds", DefaultHealthCheckSec)
	v.SetDefault("swagger_source_region", "")
	v.SetDefault("swagger_fetch_timeout_seconds", DefaultSwaggerFetchSec)
	v.SetDefault("swagger_refresh_interval_seconds", DefaultHealthCheckSec)

	port := v.GetInt("port")
	if port <= 0 || port > 65535 {
		return nil, fmt.Errorf("invalid PORT %d", port)
	}
	maxBody := v.GetInt64("max_json_body_bytes")
	if maxBody <= 0 {
		return nil, fmt.Errorf("invalid MAX_JSON_BODY_BYTES %d", maxBody)
	}
	fetchSec := v.GetInt("swagger_fetch_timeout_seconds")
	if fetchSec <= 0 {
		return nil, fmt.Errorf("invalid SWAGGER_FETCH_TIMEOUT_SECONDS %d", fetchSec)
	}
	refreshSec := v.GetInt("swagger_refresh_interval_seconds")
	if refreshSec <= 0 {
		refreshSec = DefaultHealthCheckSec
	}

	return &GatewayConfig{
		Port:                   port,
		LogLevel:               v.GetString("log_level"),
		RegionsConfigPath:      v.GetString("regions_config_path"),
		MaxJSONBodyBytes:       maxBody,
		HealthCheckInterval:    time.Duration(v.GetInt("health_check_interval_seconds")) * time.Second,
		SwaggerSourceRegion:    v.GetString("swagger_source_region"),
		SwaggerFetchTimeout:    time.Duration(fetchSec) * time.Second,
		SwaggerRefreshInterval: time.Duration(refreshSec) * time.Second,
	}, nil
}
