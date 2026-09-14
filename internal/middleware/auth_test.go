package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEnforcer(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	h := Enforcer(true)(next)

	tests := []struct {
		name    string
		headers map[string]string
		want    int
	}{
		{"missing both", nil, http.StatusUnauthorized},
		{"missing user gid", map[string]string{HeaderCMPProjectID: "p1"}, http.StatusUnauthorized},
		{"missing project id", map[string]string{HeaderCMPUserGID: "u1"}, http.StatusUnauthorized},
		{"blank header", map[string]string{HeaderCMPUserGID: "  ", HeaderCMPProjectID: "p1"}, http.StatusUnauthorized},
		{"valid assertion", map[string]string{HeaderCMPUserGID: "u1", HeaderCMPProjectID: "p1"}, http.StatusOK},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/apis/v2beta1/pipelines", nil)
			for k, v := range tt.headers {
				req.Header.Set(k, v)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			assert.Equal(t, tt.want, rec.Code)
		})
	}

	t.Run("disabled mode passes everything", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		rec := httptest.NewRecorder()
		Enforcer(false)(next).ServeHTTP(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code)
	})

	t.Run("forwards Kubeflow-UserId from user gid", func(t *testing.T) {
		var captured string
		spy := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			captured = r.Header.Get(HeaderKubeflowUser)
			w.WriteHeader(http.StatusOK)
		})
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set(HeaderCMPUserGID, "user-gid-42")
		req.Header.Set(HeaderCMPProjectID, "proj-1")
		Enforcer(true)(spy).ServeHTTP(httptest.NewRecorder(), req)
		assert.Equal(t, "user-gid-42", captured)
	})

	t.Run("does not overwrite existing Kubeflow-UserId", func(t *testing.T) {
		var captured string
		spy := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			captured = r.Header.Get(HeaderKubeflowUser)
			w.WriteHeader(http.StatusOK)
		})
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set(HeaderCMPUserGID, "user-gid-42")
		req.Header.Set(HeaderCMPProjectID, "proj-1")
		req.Header.Set(HeaderKubeflowUser, "already-set")
		Enforcer(true)(spy).ServeHTTP(httptest.NewRecorder(), req)
		assert.Equal(t, "already-set", captured)
	})
}

func TestServiceKey(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	h := ServiceKey("secret-key")(next)

	tests := []struct {
		name  string
		key   string
		valid bool
		want  int
	}{
		{"correct key", "secret-key", true, http.StatusOK},
		{"wrong key", "wrong", true, http.StatusUnauthorized},
		{"missing key", "", true, http.StatusUnauthorized},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPut, "/apis/v2beta1/projects", nil)
			if tt.key != "" {
				req.Header.Set(HeaderServiceKey, tt.key)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			assert.Equal(t, tt.want, rec.Code)
		})
	}

	t.Run("invalid JSON response shape", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPut, "/", nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		assert.Equal(t, "application/json", rec.Header().Get("Content-Type"))
		require.Contains(t, rec.Body.String(), "service_key_required")
	})
}
