// Package proxy dispatches requests to per-region KFP apiservers through
// one httputil.ReverseProxy per region.
package proxy

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"

	"github.com/golang/glog"
	"github.com/viettel-ai-platform/pipeline-core-service/internal/namespace"
	"github.com/viettel-ai-platform/pipeline-core-service/internal/region"
)

// AllRegionsValue is the sentinel that triggers a multi-region fan-out.
// Clients supply it as X-Region: all or ?region=all on list (GET) requests.
const AllRegionsValue = region.AllRegions

// fanoutResult captures the outcome of one regional call.
type fanoutResult struct {
	regionName string
	body       []byte
	status     int // 0 means the region was skipped (project not provisioned)
	err        error
}

// FanoutMiddleware returns a middleware that intercepts GET requests
// carrying X-Region: all or ?region=all, fans the request out to every
// configured region concurrently, injects "region" into every list item of
// each response, merges all items into a single list, and returns a combined
// response. Non-fan-out requests are forwarded to next unchanged.
//
// Namespace injection: if the request carries a Project-ID header, each
// regional sub-request resolves the project's namespace in that region and
// injects it as ?namespace=. Regions where the project is not provisioned
// are silently skipped. If nsResolver is nil, no namespace injection is
// performed (useful for tests / admin use-cases).
//
// Partial results: if some regions fail or are skipped, the successfully
// collected items are returned. Failed region names are listed in a
// "failed_regions" top-level field of the merged response.
func FanoutMiddleware(
	d *Dispatcher,
	nsResolver namespace.Store,
	enforcerRequired bool,
) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !isFanoutRequest(r) {
				next.ServeHTTP(w, r)
				return
			}
			if r.Method != http.MethodGet {
				(&region.APIError{
					Status:  http.StatusMethodNotAllowed,
					Code:    "method_not_allowed",
					Message: "Multi-region fan-out is only supported for GET (list) requests.",
				}).Write(w)
				return
			}

			// Enforce the same auth assertion as the Enforcer middleware.
			if enforcerRequired {
				if strings.TrimSpace(r.Header.Get("X-CMP-User-GID")) == "" ||
					strings.TrimSpace(r.Header.Get("X-CMP-Project-ID")) == "" {
					(&region.APIError{
						Status:  http.StatusUnauthorized,
						Code:    "enforcer_assertion_required",
						Message: "Requests must carry X-CMP-User-GID and X-CMP-Project-ID assertion headers from the enforcer.",
					}).Write(w)
					return
				}
			}

			targets := d.Targets()
			if len(targets) == 0 {
				(&region.APIError{
					Status:  http.StatusServiceUnavailable,
					Code:    "no_regions",
					Message: "No regions are configured.",
				}).Write(w)
				return
			}

			projectID := strings.TrimSpace(r.Header.Get("Project-ID"))
			// Strip region=all so sub-requests don't carry the sentinel.
			rawQuery := region.StripQueryParam(r.URL.RawQuery, region.QueryParam)

			resultCh := make(chan fanoutResult, len(targets))
			var wg sync.WaitGroup
			for name, t := range targets {
				wg.Add(1)
				go func(regionName string, tgt *Target) {
					defer wg.Done()
					body, status, err := doFanoutCall(r.Context(), r, rawQuery, regionName, tgt, projectID, nsResolver)
					resultCh <- fanoutResult{regionName: regionName, body: body, status: status, err: err}
				}(name, t)
			}
			wg.Wait()
			close(resultCh)

			var results []fanoutResult
			for res := range resultCh {
				results = append(results, res)
			}

			merged, err := mergeListResponses(results)
			if err != nil {
				(&region.APIError{
					Status:  http.StatusInternalServerError,
					Code:    "fanout_merge_error",
					Message: err.Error(),
				}).Write(w)
				return
			}

			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(merged)
		})
	}
}

// isFanoutRequest reports whether the request carries the "all" sentinel in
// the X-Region header or the ?region= query parameter.
func isFanoutRequest(r *http.Request) bool {
	if region.IsAllRegions(r.Header.Get(region.HeaderName)) {
		return true
	}
	for _, v := range r.URL.Query()[region.QueryParam] {
		if region.IsAllRegions(v) {
			return true
		}
	}
	return false
}

// doFanoutCall makes a single GET request to one regional KFP apiserver.
// Returns (nil, 0, nil) when the region should be silently skipped (e.g.
// project not provisioned there).
func doFanoutCall(
	ctx context.Context,
	orig *http.Request,
	rawQuery string,
	regionName string,
	t *Target,
	projectID string,
	nsResolver namespace.Store,
) ([]byte, int, error) {
	q := rawQuery

	// Resolve the project's namespace in this region if requested.
	if projectID != "" && nsResolver != nil {
		ns, ok, dbErr := nsResolver.Resolve(ctx, projectID, regionName)
		if dbErr {
			return nil, http.StatusInternalServerError,
				fmt.Errorf("region %q: namespace lookup failed", regionName)
		}
		if !ok {
			glog.V(2).Infof("fanout: project %q not provisioned in region %q, skipping", projectID, regionName)
			return nil, 0, nil // sentinel: skip this region
		}
		q = region.SetQueryParam(q, "namespace", ns)
	}

	reqURL := *t.URL
	reqURL.Path = orig.URL.Path
	reqURL.RawQuery = q

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL.String(), nil)
	if err != nil {
		return nil, 0, fmt.Errorf("region %q: build request: %w", regionName, err)
	}
	// Forward the audit/identity headers the upstream KFP expects.
	for _, h := range []string{"Authorization", "Kubeflow-UserId"} {
		if v := orig.Header.Get(h); v != "" {
			req.Header.Set(h, v)
		}
	}
	// Keep X-Region for upstream audit trails.
	req.Header.Set(region.HeaderName, regionName)

	resp, err := t.Client.Do(req)
	if err != nil {
		glog.Errorf("fanout: region %q request failed: %v", regionName, err)
		return nil, 0, fmt.Errorf("region %q: %w", regionName, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, 0, fmt.Errorf("region %q: read response: %w", regionName, err)
	}
	return body, resp.StatusCode, nil
}

// mergeListResponses combines list responses from multiple regions into a
// single JSON object. It locates the first top-level array field in each
// successful 200 response, injects "region" into every element, concatenates
// all elements, and sums the total_size values. Pagination tokens are dropped
// because cross-region cursors are not defined. Failed regions are listed in
// a "failed_regions" field.
func mergeListResponses(results []fanoutResult) ([]byte, error) {
	var (
		listKey       string
		allItems      []json.RawMessage
		totalSize     int64
		failedRegions []string
	)

	for _, res := range results {
		if res.status == 0 {
			// Silently skipped region (not provisioned).
			continue
		}
		if res.err != nil {
			glog.Errorf("fanout: %v", res.err)
			failedRegions = append(failedRegions, res.regionName)
			continue
		}
		if res.status != http.StatusOK {
			glog.Errorf("fanout: region %q returned HTTP %d: %s", res.regionName, res.status, res.body)
			failedRegions = append(failedRegions, res.regionName)
			continue
		}

		var obj map[string]json.RawMessage
		if err := json.Unmarshal(res.body, &obj); err != nil {
			glog.Errorf("fanout: region %q: parse response: %v", res.regionName, err)
			failedRegions = append(failedRegions, res.regionName)
			continue
		}

		// Accumulate total_size.
		if raw, ok := obj["total_size"]; ok {
			var n int64
			if err := json.Unmarshal(raw, &n); err == nil {
				totalSize += n
			}
		}

		// Find the list field (first array-typed value).
		key, items, ok := findListField(obj)
		if !ok {
			// No array field — treat as empty (may be an error response body).
			glog.Warningf("fanout: region %q: no list field found in response", res.regionName)
			continue
		}
		if listKey == "" {
			listKey = key
		}
		// Inject "region" into every item.
		for _, item := range items {
			allItems = append(allItems, injectRegionIntoItem(item, res.regionName))
		}
	}

	if listKey == "" {
		listKey = "items" // fallback when all regions were skipped/failed
	}

	// Build the merged response object.
	type mergedResponse struct {
		TotalSize     int64    `json:"total_size"`
		FailedRegions []string `json:"failed_regions,omitempty"`
	}
	// We build the object manually to keep the list field name dynamic.
	itemsJSON, err := json.Marshal(allItems)
	if err != nil {
		return nil, fmt.Errorf("marshal merged items: %w", err)
	}
	meta := mergedResponse{TotalSize: totalSize, FailedRegions: failedRegions}
	metaJSON, err := json.Marshal(meta)
	if err != nil {
		return nil, fmt.Errorf("marshal merged metadata: %w", err)
	}
	// Stitch: {"<key>": [...], "total_size": N, "failed_regions": [...]}
	// metaJSON is {"total_size":N,...}; strip outer braces and append.
	inner := metaJSON[1 : len(metaJSON)-1] // drop { and }
	out := make([]byte, 0, 2+len(listKey)+3+len(itemsJSON)+1+len(inner)+1)
	out = append(out, '{')
	out = appendJSONString(out, listKey)
	out = append(out, ':')
	out = append(out, itemsJSON...)
	if len(inner) > 0 {
		out = append(out, ',')
		out = append(out, inner...)
	}
	out = append(out, '}')
	return out, nil
}

// findListField returns the key and parsed elements of the first top-level
// JSON array field in obj, excluding well-known scalar fields.
func findListField(obj map[string]json.RawMessage) (key string, items []json.RawMessage, ok bool) {
	skip := map[string]bool{
		"total_size":      true,
		"next_page_token": true,
	}
	for k, raw := range obj {
		if skip[k] {
			continue
		}
		var arr []json.RawMessage
		if err := json.Unmarshal(raw, &arr); err == nil {
			return k, arr, true
		}
	}
	return "", nil, false
}

// injectRegionIntoItem adds or overwrites the top-level "region" field in a
// JSON object. Non-object items are returned unchanged.
func injectRegionIntoItem(item json.RawMessage, regionName string) json.RawMessage {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(item, &obj); err != nil || obj == nil {
		return item
	}
	obj["region"], _ = json.Marshal(regionName)
	out, err := json.Marshal(obj)
	if err != nil {
		return item
	}
	return out
}

// appendJSONString appends a JSON-encoded string (with quotes) to dst.
func appendJSONString(dst []byte, s string) []byte {
	b, _ := json.Marshal(s)
	return append(dst, b...)
}
