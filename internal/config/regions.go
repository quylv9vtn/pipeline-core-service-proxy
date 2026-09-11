package config

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"regexp"
	"sort"
	"sync/atomic"
)

// RegionConfig is one region's upstream apiserver endpoint.
type RegionConfig struct {
	Endpoint           string `json:"endpoint"`
	InsecureSkipVerify bool   `json:"insecure_skip_verify,omitempty"`
}

// RegionsFile is the on-disk (ConfigMap) document:
//
//	{"regions": {"vn-north-1": {"endpoint": "http://10.24.10.26:31132"}}}
type RegionsFile struct {
	Regions map[string]RegionConfig `json:"regions"`
}

var regionNameRE = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// LoadRegionsFile reads and validates a regions document.
func LoadRegionsFile(path string) (map[string]RegionConfig, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read regions config %q: %w", path, err)
	}
	var f RegionsFile
	if err := json.Unmarshal(raw, &f); err != nil {
		return nil, fmt.Errorf("parse regions config %q: %w", path, err)
	}
	if err := ValidateRegions(f.Regions); err != nil {
		return nil, fmt.Errorf("invalid regions config %q: %w", path, err)
	}
	return f.Regions, nil
}

// ValidateRegions enforces: at least one region, DNS-style lowercase names,
// and http/https endpoints with a host.
func ValidateRegions(regions map[string]RegionConfig) error {
	if len(regions) == 0 {
		return fmt.Errorf("at least one region must be configured")
	}
	names := make([]string, 0, len(regions))
	for name := range regions {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if !regionNameRE.MatchString(name) {
			return fmt.Errorf("region %q: name must match %s", name, regionNameRE.String())
		}
		cfg := regions[name]
		u, err := url.Parse(cfg.Endpoint)
		if err != nil || u.Scheme != "http" && u.Scheme != "https" || u.Host == "" {
			return fmt.Errorf("region %q: endpoint %q must be an absolute http(s) URL with a host", name, cfg.Endpoint)
		}
	}
	return nil
}

// Store holds the immutable region snapshot; swapped atomically on hot reload.
type Store struct {
	v atomic.Pointer[map[string]RegionConfig]
}

func NewStore(regions map[string]RegionConfig) *Store {
	s := &Store{}
	s.v.Store(&regions)
	return s
}

// Swap replaces the snapshot with an already-validated map.
func (s *Store) Swap(regions map[string]RegionConfig) { s.v.Store(&regions) }

// Lookup returns the config for an exact (post-TrimSpace) region name.
func (s *Store) Lookup(name string) (RegionConfig, bool) {
	cfg, ok := (*s.v.Load())[name]
	return cfg, ok
}

// Names returns the sorted region names.
func (s *Store) Names() []string {
	m := *s.v.Load()
	names := make([]string, 0, len(m))
	for name := range m {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// Snapshot returns the current region map (do not mutate).
func (s *Store) Snapshot() map[string]RegionConfig { return *s.v.Load() }
