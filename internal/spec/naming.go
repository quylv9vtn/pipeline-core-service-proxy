package spec

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

// The generated name tail matches KFP's pipelineNamePattern (lowercase
// alphanumeric and '-'). suffixLen=6 gives 36^6 ≈ 2×10^9 combinations, so a
// collision against the few hundred pipelines a project realistically has
// is negligible.
const (
	suffixChars = "abcdefghijklmnopqrstuvwxyz0123456789"
	suffixLen   = 6
	// Names are capped at 128 by KFP; keep the slug part well under so
	// suffix + separator always fit.
	maxSlugLen = 100
)

// randSuffix returns a cryptographically random [a-z0-9]{suffixLen} tail.
func randSuffix() string {
	out := make([]byte, suffixLen)
	max := big.NewInt(int64(len(suffixChars)))
	for i := range out {
		n, err := rand.Int(rand.Reader, max)
		if err != nil {
			// crypto/rand failing means the entropy source is broken; there
			// is no safe fallback short of failing the request.
			panic(fmt.Sprintf("crypto/rand unavailable: %v", err))
		}
		out[i] = suffixChars[n.Int64()]
	}
	return string(out)
}

var slugSeparators = regexp.MustCompile(`[^a-z0-9]+`)

// Slugify folds a display name into a KFP-safe slug: lowercase, map đ→d,
// NFKD-decompose and drop combining marks (accent folding), then collapse
// every non-[a-z0-9] run into a single '-'.
func Slugify(s string) string {
	s = strings.Map(func(r rune) rune {
		if r == 'đ' || r == 'Đ' {
			return 'd' // đ is a base letter, not an accent — NFKD keeps it
		}
		return unicode.ToLower(r)
	}, s)
	s = norm.NFKD.String(s)
	var b strings.Builder
	for _, r := range s {
		if unicode.Is(unicode.Mn, r) {
			continue // combining accent left by the decomposition
		}
		b.WriteRune(r)
	}
	slug := strings.Trim(slugSeparators.ReplaceAllString(b.String(), "-"), "-")
	if len(slug) > maxSlugLen {
		slug = strings.Trim(slug[:maxSlugLen], "-")
	}
	if slug == "" {
		return "pipeline"
	}
	return slug
}

// specStem is the display-name fallback when neither display_name nor name
// is sent: the uploaded filename without its extension.
func specStem(filename string) string {
	return strings.TrimSuffix(filename, path.Ext(filename))
}

// PipelineLister lists the pipeline display names that already exist in one
// project namespace (used for display_name uniqueness).
type PipelineLister interface {
	ListDisplayNames(ctx context.Context, regionName, namespace string) (map[string]struct{}, error)
}

// KFPLister implements PipelineLister against the region's KFP
// list-pipelines API, through the same transport/TLS the proxy uses.
type KFPLister struct {
	// Target resolves a region to its KFP base URL and HTTP client.
	Target func(regionName string) (baseURL *url.URL, client *http.Client, ok bool)
}

// listPageSize and listPageBudget bound the listing: 100 pages × 100 items
// covers 10k pipelines per project, far past realistic sizes, so a paging
// loop can never run away.
const (
	listPageSize   = 100
	listPageBudget = 100
)

func (l *KFPLister) ListDisplayNames(ctx context.Context, regionName, namespace string) (map[string]struct{}, error) {
	base, client, ok := l.Target(regionName)
	if !ok {
		return nil, fmt.Errorf("region %q is not loaded", regionName)
	}
	names := map[string]struct{}{}
	token := ""
	for page := 0; ; page++ {
		if page >= listPageBudget {
			return nil, fmt.Errorf("pipeline listing exceeded the %d-page budget", listPageBudget)
		}
		q := url.Values{}
		q.Set("namespace", namespace)
		q.Set("page_size", fmt.Sprint(listPageSize))
		if token != "" {
			q.Set("page_token", token)
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet,
			base.JoinPath("/apis/v2beta1/pipelines").String()+"?"+q.Encode(), nil)
		if err != nil {
			return nil, err
		}
		resp, err := client.Do(req)
		if err != nil {
			return nil, err
		}
		body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
		_ = resp.Body.Close()
		if err != nil {
			return nil, err
		}
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("list pipelines returned status %d", resp.StatusCode)
		}
		var data struct {
			Pipelines []struct {
				DisplayName string `json:"display_name"`
			} `json:"pipelines"`
			NextPageToken string `json:"next_page_token"`
		}
		if err := json.Unmarshal(body, &data); err != nil {
			return nil, fmt.Errorf("decode list-pipelines response: %w", err)
		}
		for _, p := range data.Pipelines {
			names[p.DisplayName] = struct{}{}
		}
		if data.NextPageToken == "" {
			return names, nil
		}
		token = data.NextPageToken
	}
}

// firstField returns the first non-empty value among the named form fields.
func firstField(fields map[string][]string, names ...string) string {
	for _, n := range names {
		if v, ok := fields[n]; ok && len(v) > 0 && v[0] != "" {
			return v[0]
		}
	}
	return ""
}
