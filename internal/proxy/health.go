package proxy

import (
	"context"
	"net/http"
	"sync"
	"time"

	"github.com/golang/glog"

	"github.com/viettel-ai-platform/pipeline-core-service/internal/metrics"
)

// Status is the last observed health of one region.
type Status struct {
	Checked   bool       `json:"-"`
	Healthy   *bool      `json:"healthy"`
	LastCheck *time.Time `json:"last_check"`
	LastError string     `json:"last_error"`
}

// HealthChecker probes each region's /apis/v2beta1/healthz on a ticker.
// Region health is reported on /regions only — liveness of the gateway
// itself never depends on an upstream being up.
type HealthChecker struct {
	dispatcher *Dispatcher
	interval   time.Duration
	client     *http.Client

	mu     sync.Mutex
	status map[string]Status
}

func NewHealthChecker(d *Dispatcher, interval time.Duration) *HealthChecker {
	return &HealthChecker{
		dispatcher: d,
		interval:   interval,
		client:     &http.Client{Timeout: 5 * time.Second},
		status:     map[string]Status{},
	}
}

// Snapshot copies the current health status.
func (h *HealthChecker) Snapshot() map[string]Status {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make(map[string]Status, len(h.status))
	for k, v := range h.status {
		out[k] = v
	}
	return out
}

// Run probes every interval until ctx is cancelled. A non-positive interval
// disables probing.
func (h *HealthChecker) Run(ctx context.Context) {
	if h.interval <= 0 {
		return
	}
	h.probeAll()
	t := time.NewTicker(h.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			h.probeAll()
		}
	}
}

func (h *HealthChecker) probeAll() {
	for _, target := range h.dispatcher.Targets() {
		healthy, errMsg := h.probe(target)
		now := time.Now()
		h.mu.Lock()
		prev := h.status[target.Name]
		next := Status{
			Checked:   true,
			Healthy:   &healthy,
			LastCheck: &now,
		}
		if !healthy {
			if errMsg != "" {
				next.LastError = errMsg
			} else if prev.LastError != "" {
				next.LastError = prev.LastError
			} else {
				next.LastError = "unhealthy"
			}
		}
		h.status[target.Name] = next
		h.mu.Unlock()
		if healthy {
			metrics.RegionHealthy.WithLabelValues(target.Name).Set(1)
		} else {
			metrics.RegionHealthy.WithLabelValues(target.Name).Set(0)
		}
	}
}

func (h *HealthChecker) probe(t *Target) (bool, string) {
	u := *t.URL
	u.Path = u.Path + "/apis/v2beta1/healthz"
	u.RawQuery = ""
	resp, err := h.client.Get(u.String())
	if err != nil {
		glog.Warningf("health probe region %q failed: %v", t.Name, err)
		return false, err.Error()
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		glog.Warningf("health probe region %q: status %d", t.Name, resp.StatusCode)
		return false, http.StatusText(resp.StatusCode)
	}
	return true, ""
}
