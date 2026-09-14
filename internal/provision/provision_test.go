package provision

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/mux"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- fakes ---

type fakeStore struct {
	mu    sync.Mutex
	rows  map[string]ProjectRegion
	calls []string
}

func key(projectID, region string) string { return projectID + "@" + region }

func (f *fakeStore) Upsert(_ context.Context, in PutInput) (ProjectRegion, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "upsert")
	if row, ok := f.rows[key(in.ProjectID, in.Region)]; ok {
		return row, false, nil
	}
	now := time.Now()
	row := ProjectRegion{ProjectID: in.ProjectID, RegionName: in.Region, Namespace: in.Namespace,
		Name: in.Name, Status: StatusProvisioning, CreatedAt: now, UpdatedAt: now}
	f.rows[key(in.ProjectID, in.Region)] = row
	return row, true, nil
}

func (f *fakeStore) MarkProvisioning(_ context.Context, in PutInput) (ProjectRegion, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "mark_provisioning")
	row, ok := f.rows[key(in.ProjectID, in.Region)]
	requireRow(ok)
	row.Namespace, row.Name, row.Status, row.UpdatedAt = in.Namespace, in.Name, StatusProvisioning, time.Now()
	f.rows[key(in.ProjectID, in.Region)] = row
	return row, nil
}

func (f *fakeStore) MarkStatus(_ context.Context, projectID, region, status string) (ProjectRegion, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "mark_status:"+status)
	row, ok := f.rows[key(projectID, region)]
	requireRow(ok)
	row.Status, row.UpdatedAt = status, time.Now()
	f.rows[key(projectID, region)] = row
	return row, nil
}

func (f *fakeStore) Delete(_ context.Context, projectID, region string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "delete")
	if _, ok := f.rows[key(projectID, region)]; !ok {
		return ErrNotFound
	}
	delete(f.rows, key(projectID, region))
	return nil
}

func (f *fakeStore) ListStale(_ context.Context, olderThan time.Time) ([]ProjectRegion, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []ProjectRegion
	for _, row := range f.rows {
		if row.Status == StatusProvisioning && row.UpdatedAt.Before(olderThan) {
			out = append(out, row)
		}
	}
	return out, nil
}

func requireRow(ok bool) {
	if !ok {
		panic("row missing")
	}
}

func (f *fakeStore) get(projectID, region string) ProjectRegion {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.rows[key(projectID, region)]
}

type fakeRegions struct{ endpoints map[string]string }

func (f fakeRegions) Endpoint(region string) (string, bool) {
	e, ok := f.endpoints[region]
	return e, ok
}

// newFakeKFP builds a fake regional KFP counting namespace-ensuring calls.
func newFakeKFP(failFirst bool) (*httptest.Server, *int32) {
	var calls int32
	mu := &sync.Mutex{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls++
		n := calls
		mu.Unlock()
		if r.URL.Path == "/apis/v2beta1/projects" {
			if failFirst && n == 1 {
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write([]byte(`{"error":"boom"}`))
				return
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	return srv, &calls
}

// --- tests ---

var testIn = PutInput{
	ProjectID: "0b9e6c7a-0000-4000-8000-000000000001",
	Name:      "myproj",
	Region:    "vn-north-1",
	Namespace: "myproj-pipeline",
}

func TestValidatePut(t *testing.T) {
	assert.NoError(t, ValidatePut(testIn))

	bad := testIn
	bad.Namespace = "wrong-suffix"
	assert.Error(t, ValidatePut(bad), "namespace must be {slug}-pipeline")

	long := testIn
	long.Name = "a-very-long-project-slug-that-exceeds-the-k8s-limit-x" // 54 chars
	long.Namespace = long.Name + "-pipeline"                            // 63+ chars
	assert.Error(t, ValidatePut(long), "63-char RFC1123 cap")

	longSlug := testIn
	longSlug.Name = "a-very-long-project-slug-that-exceeds-the-limit-40ch" // > 40 chars
	longSlug.Namespace = longSlug.Name + "-pipeline"
	assert.Error(t, ValidatePut(longSlug), "slug max 40 chars")

	upper := testIn
	upper.Namespace = "MyProj-Pipeline"
	assert.Error(t, ValidatePut(upper), "RFC 1123 lowercase")

	notUUID := testIn
	notUUID.ProjectID = "abc"
	assert.Error(t, ValidatePut(notUUID))
}

func TestProvisionerPut(t *testing.T) {
	kfp, calls := newFakeKFP(false)
	defer kfp.Close()

	store := &fakeStore{rows: map[string]ProjectRegion{}}
	p := New(store, fakeRegions{endpoints: map[string]string{"vn-north-1": kfp.URL}}, NewKFPClient())

	t.Run("creates and fans out", func(t *testing.T) {
		row, created, err := p.Put(context.Background(), testIn)
		require.NoError(t, err)
		assert.True(t, created)
		assert.Equal(t, StatusReady, row.Status)
		assert.Equal(t, int32(1), *calls)
	})

	t.Run("idempotent replay skips fan-out", func(t *testing.T) {
		row, created, err := p.Put(context.Background(), testIn)
		require.NoError(t, err)
		assert.False(t, created)
		assert.Equal(t, StatusReady, row.Status)
		assert.Equal(t, int32(1), *calls, "no extra upstream call")
	})

	t.Run("payload change re-provisions", func(t *testing.T) {
		changed := testIn
		changed.Name = "myproj2"
		changed.Namespace = "myproj2-pipeline"
		row, created, err := p.Put(context.Background(), changed)
		require.NoError(t, err)
		assert.False(t, created)
		assert.Equal(t, StatusReady, row.Status)
		assert.Equal(t, "myproj2-pipeline", store.get(changed.ProjectID, changed.Region).Namespace)
		assert.Equal(t, int32(2), *calls)
	})
}

func TestProvisionerFanOutFailureKeepsProvisioning(t *testing.T) {
	kfp, calls := newFakeKFP(true) // first call fails
	defer kfp.Close()

	store := &fakeStore{rows: map[string]ProjectRegion{}}
	p := New(store, fakeRegions{endpoints: map[string]string{"vn-north-1": kfp.URL}}, NewKFPClient())

	row, created, err := p.Put(context.Background(), testIn)
	require.NoError(t, err, "regional failure is not a Put error; row stays provisioning")
	assert.True(t, created)
	assert.Equal(t, StatusProvisioning, row.Status)
	assert.Equal(t, int32(1), *calls)

	// Reconciler retries and flips to ready.
	p.reconcileOnce(context.Background(), 0)
	assert.Equal(t, StatusReady, store.get(testIn.ProjectID, testIn.Region).Status)
	assert.Equal(t, int32(2), *calls)
}

func TestProvisionerUnknownRegion(t *testing.T) {
	store := &fakeStore{rows: map[string]ProjectRegion{}}
	p := New(store, fakeRegions{endpoints: map[string]string{}}, NewKFPClient())
	_, _, err := p.Put(context.Background(), testIn)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown region")
	assert.Equal(t, StatusProvisioning, store.get(testIn.ProjectID, testIn.Region).Status)
}

func TestDeleteProjectHandler(t *testing.T) {
	store := &fakeStore{rows: map[string]ProjectRegion{}}
	// Endpoint is unreachable on purpose: the row stays 'provisioning',
	// which is exactly the state the delete test needs.
	p := New(store, fakeRegions{endpoints: map[string]string{"vn-north-1": "http://127.0.0.1:1"}}, NewKFPClient())
	_, _, err := p.Put(context.Background(), testIn)
	require.NoError(t, err)

	r := mux.NewRouter()
	r.HandleFunc("/apis/v2beta1/projects/{project_id}", DeleteProject(p)).Methods(http.MethodDelete)

	req := httptest.NewRequest(http.MethodDelete, "/apis/v2beta1/projects/"+testIn.ProjectID+"?region=vn-north-1", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusNoContent, rec.Code)

	// Idempotent: second delete still 204.
	req = httptest.NewRequest(http.MethodDelete, "/apis/v2beta1/projects/"+testIn.ProjectID+"?region=vn-north-1", nil)
	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusNoContent, rec.Code)

	// Missing region → 400.
	req = httptest.NewRequest(http.MethodDelete, "/apis/v2beta1/projects/"+testIn.ProjectID, nil)
	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestPutProjectHandler(t *testing.T) {
	kfp, _ := newFakeKFP(false)
	defer kfp.Close()

	store := &fakeStore{rows: map[string]ProjectRegion{}}
	p := New(store, fakeRegions{endpoints: map[string]string{"vn-north-1": kfp.URL}}, NewKFPClient())

	r := mux.NewRouter()
	r.HandleFunc("/apis/v2beta1/projects", PutProject(p)).Methods(http.MethodPut)

	body, err := json.Marshal(testIn)
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPut, "/apis/v2beta1/projects", bytesReader(body))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusCreated, rec.Code)
	assert.Contains(t, rec.Body.String(), `"status":"ready"`)

	// Replay → 200.
	req = httptest.NewRequest(http.MethodPut, "/apis/v2beta1/projects", bytesReader(body))
	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)

	// Invalid payload → 400.
	req = httptest.NewRequest(http.MethodPut, "/apis/v2beta1/projects",
		bytesReader([]byte(`{"project_id":"nope"}`)))
	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

type byteRd struct{ b []byte }

func (r *byteRd) Read(p []byte) (int, error) {
	if len(r.b) == 0 {
		return 0, io.EOF
	}
	n := copy(p, r.b)
	r.b = r.b[n:]
	return n, nil
}

func bytesReader(b []byte) *byteRd { return &byteRd{b: b} }
