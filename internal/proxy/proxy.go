// Package proxy dispatches requests to per-region KFP apiservers through
// one httputil.ReverseProxy per region.
package proxy

import (
	"bytes"
	"crypto/tls"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/golang/glog"

	"github.com/viettel-ai-platform/pipeline-core-service/internal/config"
	"github.com/viettel-ai-platform/pipeline-core-service/internal/metrics"
	"github.com/viettel-ai-platform/pipeline-core-service/internal/region"
)

// Target is a ready-to-use upstream for one region.
type Target struct {
	Name   string
	Config config.RegionConfig
	URL    *url.URL
	Proxy  *httputil.ReverseProxy
	// Client performs direct server-to-server calls to the same upstream
	// (same transport/TLS as the proxy) — e.g. the upload handler's
	// display-name uniqueness lookup.
	Client *http.Client
}

// Dispatcher owns the current region→proxy table and forwards requests.
type Dispatcher struct {
	targets atomic.Pointer[map[string]*Target]
}

func NewDispatcher() *Dispatcher {
	d := &Dispatcher{}
	empty := map[string]*Target{}
	d.targets.Store(&empty)
	return d
}

// Reload builds proxies for the given regions and swaps the table. On any
// invalid endpoint the old table is kept and the error returned.
func (d *Dispatcher) Reload(regions map[string]config.RegionConfig) error {
	built := make(map[string]*Target, len(regions))
	for name, rc := range regions {
		t, err := newTarget(name, rc)
		if err != nil {
			return err
		}
		built[name] = t
	}
	d.targets.Store(&built)
	return nil
}

// Target returns the current target for a region.
func (d *Dispatcher) Target(name string) (*Target, bool) {
	t, ok := (*d.targets.Load())[name]
	return t, ok
}

// Targets snapshots the current table.
func (d *Dispatcher) Targets() map[string]*Target { return *d.targets.Load() }

// ValidRegions returns the sorted names of the current targets.
func (d *Dispatcher) ValidRegions() []string {
	m := d.Targets()
	names := make([]string, 0, len(m))
	for name := range m {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// ServeHTTP forwards the request to the region resolved by the middleware.
// The table is re-checked here to close the hot-reload race where a region
// disappears between validation and dispatch.
func (d *Dispatcher) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	res, ok := region.FromContext(r.Context())
	if !ok {
		(&region.APIError{
			Status:  http.StatusInternalServerError,
			Code:    "internal_error",
			Message: "No region resolved for request.",
		}).Write(w)
		return
	}
	t, ok := d.Target(res.Name)
	if !ok {
		region.UnknownRegion(res.Name, d.names()).Write(w)
		return
	}
	t.Proxy.ServeHTTP(w, r)
}

func (d *Dispatcher) names() []string {
	m := d.Targets()
	names := make([]string, 0, len(m))
	for name := range m {
		names = append(names, name)
	}
	return names
}

// newTarget builds the per-region transport and reverse proxy.
//
//   - Rewrite (not Director): safer defaults, automatic X-Forwarded-*.
//   - FlushInterval 100ms: run-log/artifact streaming with minimal lag.
//   - DisableCompression: forward the client's Accept-Encoding verbatim and
//     stream gzipped upstream responses through undecoded; the default
//     transport would silently decompress and rewrite them.
//   - No ResponseHeaderTimeout and no overall timeout: `follow=true` log
//     streams are unbounded.
func newTarget(name string, rc config.RegionConfig) (*Target, error) {
	u, err := url.Parse(rc.Endpoint)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("region %q: invalid endpoint %q", name, rc.Endpoint)
	}
	transport := &http.Transport{
		DialContext: (&net.Dialer{
			Timeout:   10 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		MaxIdleConns:          100,
		MaxIdleConnsPerHost:   20,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		DisableCompression:    true,
		ForceAttemptHTTP2:     false,
		TLSClientConfig:       &tls.Config{InsecureSkipVerify: rc.InsecureSkipVerify}, //nolint:gosec // opt-in per region for self-signed endpoints
	}
	target := &Target{Name: name, Config: rc, URL: u}
	target.Client = &http.Client{Transport: transport, Timeout: 30 * time.Second}
	target.Proxy = &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(u) // also rewrites Out.Host to the target's host
			// X-Region is intentionally forwarded: it gives the regional
			// side an audit trail of gateway-routed calls.
		},
		Transport:      transport,
		FlushInterval:  100 * time.Millisecond,
		ModifyResponse: injectRegionResponse(name),
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, perr error) {
			metrics.UpstreamErrors.WithLabelValues(name).Inc()
			glog.Errorf("proxy to region %q %s %s failed: %v", name, r.Method, r.URL.Path, perr)
			region.UpstreamUnavailable(name, perr).Write(w)
		},
	}
	return target, nil
}

// injectRegionResponse returns a ModifyResponse hook that adds a top-level
// "region" field to JSON responses. Encoded (e.g. gzip) or non-JSON responses
// are forwarded byte-identical; injection failures are silently skipped so
// the upstream payload always reaches the client.
func injectRegionResponse(regionName string) func(*http.Response) error {
	return func(resp *http.Response) error {
		// Only modify uncompressed JSON responses.
		if resp.Header.Get("Content-Encoding") != "" {
			return nil
		}
		ct := resp.Header.Get("Content-Type")
		if !isJSONContentType(ct) {
			return nil
		}
		body, err := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if err != nil {
			// Return the empty body rather than propagating; upstream error
			// is better surfaced by the status code.
			resp.Body = io.NopCloser(bytes.NewReader(body))
			return nil //nolint:nilerr
		}
		modified := region.SetTopLevelJSONKey(body, "region", regionName)
		resp.Body = io.NopCloser(bytes.NewReader(modified))
		resp.ContentLength = int64(len(modified))
		resp.Header.Set("Content-Length", strconv.Itoa(len(modified)))
		return nil
	}
}

func isJSONContentType(ct string) bool {
	mt, _, err := mime.ParseMediaType(ct)
	return err == nil && (mt == "application/json" || strings.HasSuffix(mt, "+json"))
}
