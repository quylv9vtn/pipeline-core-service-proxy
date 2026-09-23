package spec

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSlugify(t *testing.T) {
	cases := map[string]string{
		"Hello World — Training v1": "hello-world-training-v1",
		"Xin chào Việt Nam":         "xin-chao-viet-nam",
		"Đà Nẵng AI 2026":           "da-nang-ai-2026",
		"  spaces   and  --  ":      "spaces-and",
		"!!!":                       "pipeline", // nothing survives → fallback
		"":                          "pipeline",
	}
	for in, want := range cases {
		assert.Equal(t, want, Slugify(in), "input %q", in)
	}

	assert.Equal(t, 100, len(Slugify(strings.Repeat("a", 300))), "slug is capped at maxSlugLen")
}

func TestRandSuffix(t *testing.T) {
	pattern := regexp.MustCompile(`^[a-z0-9]{6}$`)
	for range [10]struct{}{} {
		assert.Regexp(t, pattern, randSuffix())
	}
}

func TestSpecStem(t *testing.T) {
	assert.Equal(t, "hello-world", specStem("hello-world.yaml"))
	assert.Equal(t, "a.b", specStem("a.b.yaml"))
	assert.Equal(t, "noext", specStem("noext"))
}

func TestKFPLister(t *testing.T) {
	t.Run("collects display names across pages", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, "/apis/v2beta1/pipelines", r.URL.Path)
			assert.Equal(t, "ns-1", r.URL.Query().Get("namespace"))
			if r.URL.Query().Get("page_token") == "" {
				_, _ = w.Write([]byte(`{"pipelines":[{"display_name":"A"}],"next_page_token":"t2"}`))
				return
			}
			_, _ = w.Write([]byte(`{"pipelines":[{"display_name":"B"}]}`))
		}))
		defer srv.Close()
		base, err := url.Parse(srv.URL)
		require.NoError(t, err)

		l := &KFPLister{Target: func(string) (*url.URL, *http.Client, bool) { return base, srv.Client(), true }}
		names, err := l.ListDisplayNames(context.Background(), "r1", "ns-1")
		require.NoError(t, err)
		assert.ElementsMatch(t, []string{"A", "B"}, keysOf(names))
	})

	t.Run("upstream error surfaces", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "boom", http.StatusInternalServerError)
		}))
		defer srv.Close()
		base, _ := url.Parse(srv.URL)
		l := &KFPLister{Target: func(string) (*url.URL, *http.Client, bool) { return base, srv.Client(), true }}
		_, err := l.ListDisplayNames(context.Background(), "r1", "ns-1")
		assert.ErrorContains(t, err, "status 500")
	})

	t.Run("unknown region", func(t *testing.T) {
		l := &KFPLister{Target: func(string) (*url.URL, *http.Client, bool) { return nil, nil, false }}
		_, err := l.ListDisplayNames(context.Background(), "rX", "ns-1")
		assert.ErrorContains(t, err, "not loaded")
	})
}

func keysOf(m map[string]struct{}) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
