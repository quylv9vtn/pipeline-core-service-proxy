package provision

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/viettel-ai-platform/pipeline-core-service/internal/metrics"
)

// KFPClient ensures namespaces exist on a regional KFP apiserver.
type KFPClient struct {
	HTTP *http.Client
}

func NewKFPClient() *KFPClient {
	return &KFPClient{HTTP: &http.Client{Timeout: 30 * time.Second}}
}

// EnsureNamespace fans out to the regional KFP: the Viettel provisioning
// extension (POST /apis/v2beta1/projects, get-or-create semantics) first,
// falling back to POST /apis/v2beta1/namespaces. Idempotent by contract —
// an existing namespace is a success, not an error. regionName labels the
// provisioning metric.
func (c *KFPClient) EnsureNamespace(ctx context.Context, regionName, endpoint, projectID, slug, namespace string) error {
	body, err := json.Marshal(map[string]string{
		"project_id": projectID,
		"name":       slug,
		"namespace":  namespace,
	})
	if err != nil {
		return err
	}
	if err := c.post(ctx, regionName, endpoint, "/apis/v2beta1/projects", body); err == nil {
		return nil
	} else if !isNotFound(err) {
		return err
	}
	// Extension not deployed (404): fall back to the plain namespace
	// provisioning contract.
	body, err = json.Marshal(map[string]string{"namespace": namespace})
	if err != nil {
		return err
	}
	return c.post(ctx, regionName, endpoint, "/apis/v2beta1/namespaces", body)
}

func (c *KFPClient) post(ctx context.Context, regionName, endpoint, path string, body []byte) error {
	url := strings.TrimSuffix(endpoint, "/") + path
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("%s: %w", url, err)
	}
	defer func() {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}()
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		metrics.ProvisioningOps.WithLabelValues(regionName, "ready").Inc()
		return nil
	}
	snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	return &upstreamError{url: url, status: resp.StatusCode, body: string(snippet)}
}

type upstreamError struct {
	url    string
	status int
	body   string
}

func (e *upstreamError) Error() string {
	return fmt.Sprintf("%s: status %d: %s", e.url, e.status, e.body)
}

func isNotFound(err error) bool {
	ue, ok := err.(*upstreamError)
	return ok && ue.status == http.StatusNotFound
}
