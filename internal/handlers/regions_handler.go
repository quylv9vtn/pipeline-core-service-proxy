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
	Endpoint  string     `json:"endpoint"`
	Healthy   *bool      `json:"healthy"`
	LastCheck *time.Time `json:"last_check"`
	LastError string     `json:"last_error"`
}

// Regions lists the configured regions with their endpoints and last
// probed health (healthy is null until the first probe completes).
func Regions(d *proxy.Dispatcher, health *proxy.HealthChecker) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		statuses := health.Snapshot()
		targets := d.Targets()

		views := make([]regionView, 0, len(targets))
		for name, t := range targets {
			st := statuses[name]
			views = append(views, regionView{
				Name:      name,
				Endpoint:  t.Config.Endpoint,
				Healthy:   st.Healthy,
				LastCheck: st.LastCheck,
				LastError: st.LastError,
			})
		}
		sort.Slice(views, func(i, j int) bool { return views[i].Name < views[j].Name })

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"regions": views})
	}
}
