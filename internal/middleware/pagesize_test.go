package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestPageSize(t *testing.T) {
	handler := PageSize()(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	cases := []struct {
		name   string
		method string
		query  string
		want   int
	}{
		{"absent passes", http.MethodGet, "", http.StatusOK},
		{"positive passes", http.MethodGet, "?page_size=10", http.StatusOK},
		{"zero rejected", http.MethodGet, "?page_size=0", http.StatusBadRequest},
		{"negative rejected", http.MethodGet, "?page_size=-5", http.StatusBadRequest},
		{"non-numeric rejected", http.MethodGet, "?page_size=abc", http.StatusBadRequest},
		{"empty value passes", http.MethodGet, "?page_size=", http.StatusOK},
		{"other params untouched", http.MethodGet, "?page_token=x&filter=y", http.StatusOK},
		{"non-GET not validated", http.MethodPost, "?page_size=0", http.StatusOK},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, httptest.NewRequest(tc.method, "/apis/v2beta1/experiments"+tc.query, nil))
			assert.Equal(t, tc.want, rec.Code)
			if tc.want == http.StatusBadRequest {
				assert.Contains(t, rec.Body.String(), "invalid_page_size")
			}
		})
	}
}
