// Package config loads gateway settings from the environment and the
// regions file (mounted from a ConfigMap in Kubernetes).
//
// Environment loading mirrors the KFP apiserver convention (viper
// AutomaticEnv): PORT, LOG_LEVEL, REGIONS_CONFIG_PATH, MAX_JSON_BODY_BYTES,
// HEALTH_CHECK_INTERVAL_SECONDS, SWAGGER_SOURCE_REGION,
// SWAGGER_FETCH_TIMEOUT_SECONDS, DATABASE_URL, SERVICE_KEY,
// ENFORCER_REQUIRED, NAMESPACE_CACHE_TTL_SECONDS,
// PROVISION_RECONCILE_INTERVAL_SECONDS, REGIONS_EXPOSE_ENDPOINTS.
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

	DefaultNamespaceCacheTTLSec        = 60
	DefaultProvisionReconcileSec       = 30
	DefaultRegionsExposeEndpoints      = false
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

	// Database for project_regions mapping and the flavor/image catalog.
	DSN string // required; empty disables DB-backed features
	// ServiceKey guards provisioning and admin endpoints (X-Service-Key).
	ServiceKey string // required
	// EnforcerRequired rejects requests without X-CMP-* assertion headers.
	// Fail-closed default: true.
	EnforcerRequired bool
	// NamespaceCacheTTL is the positive lookup cache TTL (negative entries
	// live for TTL/10, minimum 1s).
	NamespaceCacheTTL time.Duration
	// ProvisionReconcileInterval drives the background retry of
	// project_regions rows stuck in 'provisioning'. <= 0 disables it.
	ProvisionReconcileInterval time.Duration
	// RegionsExposeEndpoints controls whether GET /regions includes the
	// internal upstream endpoint field. Default false (hidden from FE).
	RegionsExposeEndpoints bool
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
	v.SetDefault("enforcer_required", true)
	v.SetDefault("namespace_cache_ttl_seconds", DefaultNamespaceCacheTTLSec)
	v.SetDefault("provision_reconcile_interval_seconds", DefaultProvisionReconcileSec)
	v.SetDefault("regions_expose_endpoints", DefaultRegionsExposeEndpoints)

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
	dsn := v.GetString("database_url")
	if dsn == "" {
		return nil, fmt.Errorf("DATABASE_URL is required (PostgreSQL DSN)")
	}
	serviceKey := v.GetString("service_key")
	if serviceKey == "" {
		return nil, fmt.Errorf("SERVICE_KEY is required (guards provisioning and admin endpoints)")
	}
	nsCacheTTL := time.Duration(v.GetInt("namespace_cache_ttl_seconds")) * time.Second
	if nsCacheTTL <= 0 {
		nsCacheTTL = DefaultNamespaceCacheTTLSec * time.Second
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

		DSN:                        dsn,
		ServiceKey:                 serviceKey,
		EnforcerRequired:           v.GetBool("enforcer_required"),
		NamespaceCacheTTL:          nsCacheTTL,
		ProvisionReconcileInterval: time.Duration(v.GetInt("provision_reconcile_interval_seconds")) * time.Second,
		RegionsExposeEndpoints:     v.GetBool("regions_expose_endpoints"),
	}, nil
}
