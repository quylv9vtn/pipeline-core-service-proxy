package handlers

import (
	"encoding/json"
	"net/http"
	"sort"
	"time"

	"github.com/viettel-ai-platform/pipeline-core-service/internal/proxy"
)

type regionView struct {
	Name      string     `json:"name"`
	Endpoint  string     `json:"endpoint,omitempty"`
	Healthy   *bool      `json:"healthy"`
	LastCheck *time.Time `json:"last_check"`
	LastError string     `json:"last_error"`
}

// Regions lists the configured regions with last probed health.
// When exposeEndpoints is false the internal upstream endpoint field is
// omitted (default: hidden from FE to avoid leaking internal service URLs).
func Regions(d *proxy.Dispatcher, health *proxy.HealthChecker, exposeEndpoints bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		statuses := health.Snapshot()
		targets := d.Targets()

		views := make([]regionView, 0, len(targets))
		for name, t := range targets {
			st := statuses[name]
			v := regionView{
				Name:      name,
				Healthy:   st.Healthy,
				LastCheck: st.LastCheck,
				LastError: st.LastError,
			}
			if exposeEndpoints {
				v.Endpoint = t.Config.Endpoint
			}
			views = append(views, v)
		}
		sort.Slice(views, func(i, j int) bool { return views[i].Name < views[j].Name })

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"regions": views})
	}
}
