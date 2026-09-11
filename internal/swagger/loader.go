package swagger

import (
	"context"
	_ "embed"
	"io"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/golang/glog"

	"github.com/viettel-ai-platform/pipeline-core-service/internal/proxy"
)

//go:embed fallback/kfp_api_v2beta1.swagger.json
var fallbackSpec []byte

const specPath = "/swagger/v2beta1/swagger.json"

// SpecProvider serves the region-annotated swagger spec. At startup it
// falls back to the embedded copy of the upstream spec (transformed);
// concurrently it fetches the live spec from a region and hot-swaps once
// the fetch succeeds, then refreshes periodically so API changes in the
// fork are picked up without a gateway rebuild.
type SpecProvider struct {
	dispatcher      *Dispatcher
	sourceRegion    string
	fetchTimeout    time.Duration
	refreshInterval time.Duration

	current atomic.Pointer[[]byte]
}

// Dispatcher aliases the proxy dispatcher type.
type Dispatcher = proxy.Dispatcher

func NewSpecProvider(d *Dispatcher, sourceRegion string, fetchTimeout, refreshInterval time.Duration) *SpecProvider {
	p := &SpecProvider{
		dispatcher:   d,
		sourceRegion: sourceRegion,
		fetchTimeout: fetchTimeout,
	}
	// Immediately serve the transformed fallback so /swagger works even
	// with every region down.
	fallback := p.transform(fallbackSpec)
	p.current.Store(&fallback)
	p.refreshInterval = refreshInterval
	return p
}

// Run fetches and refreshes the live spec until ctx is cancelled.
func (p *SpecProvider) Run(ctx context.Context) {
	t := time.NewTicker(p.refreshInterval)
	defer t.Stop()
	p.tryFetch()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			p.tryFetch()
		}
	}
}

func (p *SpecProvider) tryFetch() {
	endpoint, ok := p.pickSource()
	if !ok {
		return
	}
	client := &http.Client{Timeout: p.fetchTimeout}
	resp, err := client.Get(endpoint + specPath)
	if err != nil {
		glog.Warningf("fetch swagger spec from region endpoint %s: %v", endpoint, err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		glog.Warningf("fetch swagger spec from %s: status %d", endpoint+specPath, resp.StatusCode)
		return
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		glog.Warningf("read swagger spec from %s: %v", endpoint, err)
		return
	}
	spec := p.transform(body)
	p.current.Store(&spec)
	glog.Infof("swagger spec loaded from %s (%d bytes)", endpoint+specPath, len(body))
}

// pickSource resolves the swagger source region to an endpoint.
func (p *SpecProvider) pickSource() (string, bool) {
	targets := p.dispatcher.Targets()
	if len(targets) == 0 {
		return "", false
	}
	if p.sourceRegion != "" {
		if t, ok := targets[p.sourceRegion]; ok {
			return t.Config.Endpoint, true
		}
		glog.Warningf("SWAGGER_SOURCE_REGION %q not configured; using first region", p.sourceRegion)
	}
	// Deterministic pick: alphabetically first region.
	first := ""
	for name := range targets {
		if first == "" || name < first {
			first = name
		}
	}
	return targets[first].Config.Endpoint, true
}

func (p *SpecProvider) transform(spec []byte) []byte {
	valid := p.dispatcher.ValidRegions()
	out, err := InjectRegionParam(spec, valid)
	if err != nil {
		// The fallback spec is known-good; a bad live fetch is logged and
		// the previous spec stays served.
		glog.Errorf("transform swagger spec: %v", err)
		return spec
	}
	return out
}

// Current returns the spec bytes to serve.
func (p *SpecProvider) Current() []byte { return *p.current.Load() }
