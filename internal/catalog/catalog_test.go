package catalog

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gorilla/mux"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/viettel-ai-platform/pipeline-core-service/internal/region"
)

type fakeStore struct {
	flavors []Flavor
	images  []Image
}

func (f *fakeStore) ListFlavors(_ context.Context, regionName string, enabledOnly bool) ([]Flavor, error) {
	out := []Flavor{}
	for _, fl := range f.flavors {
		if fl.RegionName == regionName && (!enabledOnly || fl.Status == StatusEnabled) {
			out = append(out, fl)
		}
	}
	return out, nil
}

func (f *fakeStore) GetFlavor(_ context.Context, id string) (Flavor, error) {
	for _, fl := range f.flavors {
		if fl.ID == id {
			return fl, nil
		}
	}
	return Flavor{}, ErrNotFound
}

func (f *fakeStore) CreateFlavor(_ context.Context, fl Flavor) (Flavor, error) {
	fl.CreatedAt = time.Now()
	fl.UpdatedAt = fl.CreatedAt
	f.flavors = append(f.flavors, fl)
	return fl, nil
}

func (f *fakeStore) UpdateFlavor(_ context.Context, fl Flavor) (Flavor, error) {
	for i, cur := range f.flavors {
		if cur.ID == fl.ID {
			fl.CreatedAt = cur.CreatedAt
			fl.UpdatedAt = time.Now()
			f.flavors[i] = fl
			return fl, nil
		}
	}
	return Flavor{}, ErrNotFound
}

func (f *fakeStore) DeleteFlavor(_ context.Context, id string) error {
	for i, cur := range f.flavors {
		if cur.ID == id {
			f.flavors = append(f.flavors[:i], f.flavors[i+1:]...)
			return nil
		}
	}
	return ErrNotFound
}

func (f *fakeStore) ListImages(_ context.Context, regionName string, enabledOnly bool) ([]Image, error) {
	out := []Image{}
	for _, im := range f.images {
		if im.RegionName == regionName && (!enabledOnly || im.Status == StatusEnabled) {
			out = append(out, im)
		}
	}
	return out, nil
}

func (f *fakeStore) GetImage(_ context.Context, id string) (Image, error) {
	for _, im := range f.images {
		if im.ID == id {
			return im, nil
		}
	}
	return Image{}, ErrNotFound
}

func (f *fakeStore) CreateImage(_ context.Context, im Image) (Image, error) {
	im.CreatedAt = time.Now()
	im.UpdatedAt = im.CreatedAt
	f.images = append(f.images, im)
	return im, nil
}

func (f *fakeStore) UpdateImage(_ context.Context, im Image) (Image, error) {
	for i, cur := range f.images {
		if cur.ID == im.ID {
			im.CreatedAt = cur.CreatedAt
			im.UpdatedAt = time.Now()
			f.images[i] = im
			return im, nil
		}
	}
	return Image{}, ErrNotFound
}

func (f *fakeStore) DeleteImage(_ context.Context, id string) error {
	for i, cur := range f.images {
		if cur.ID == id {
			f.images = append(f.images[:i], f.images[i+1:]...)
			return nil
		}
	}
	return ErrNotFound
}

func TestValidateFlavor(t *testing.T) {
	gpu := GPUModelH200
	valid := Flavor{Name: "f1", RegionName: "vn-north-1", CPU: 4, MemoryGiB: 16, Status: StatusEnabled}

	t.Run("valid without gpu", func(t *testing.T) {
		f := valid
		assert.NoError(t, ValidateFlavor(f))
	})
	t.Run("valid with gpu", func(t *testing.T) {
		f := valid
		f.GPUModel, f.GPUCount = &gpu, 2
		assert.NoError(t, ValidateFlavor(f))
	})
	t.Run("gpu_model without count rejected", func(t *testing.T) {
		f := valid
		f.GPUModel = &gpu
		assert.Error(t, ValidateFlavor(f))
	})
	t.Run("count without gpu_model rejected", func(t *testing.T) {
		f := valid
		f.GPUCount = 2
		assert.Error(t, ValidateFlavor(f))
	})
	t.Run("unknown gpu model rejected", func(t *testing.T) {
		f := valid
		bad := "v100"
		f.GPUModel, f.GPUCount = &bad, 1
		assert.Error(t, ValidateFlavor(f))
	})
	t.Run("cpu below 1 rejected", func(t *testing.T) {
		f := valid
		f.CPU = 0
		assert.Error(t, ValidateFlavor(f))
	})
	t.Run("missing name rejected", func(t *testing.T) {
		f := valid
		f.Name = "  "
		assert.Error(t, ValidateFlavor(f))
	})
}

func TestValidateImage(t *testing.T) {
	valid := Image{Name: "img", RegionName: "vn-north-1", Version: "v1.0", ImageURL: "registry/img:v1.0"}
	assert.NoError(t, ValidateImage(valid))
	missing := valid
	missing.ImageURL = ""
	assert.Error(t, ValidateImage(missing))
}

func testRegionCtx(r *http.Request, regionName string) *http.Request {
	return r.WithContext(region.IntoContext(r.Context(), &region.Result{Name: regionName}))
}

func TestFEListFlavors(t *testing.T) {
	store := &fakeStore{flavors: []Flavor{
		{ID: "1", Name: "enabled-a", RegionName: "vn-north-1", CPU: 2, MemoryGiB: 4, Status: StatusEnabled},
		{ID: "2", Name: "disabled-b", RegionName: "vn-north-1", CPU: 8, MemoryGiB: 8, Status: StatusDisabled},
		{ID: "3", Name: "other-region", RegionName: "vn-north-2", CPU: 1, MemoryGiB: 1, Status: StatusEnabled},
	}}

	req := testRegionCtx(httptest.NewRequest(http.MethodGet, "/apis/v2beta1/flavors", nil), "vn-north-1")
	rec := httptest.NewRecorder()
	FEListFlavors(store)(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	var got []Flavor
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	require.Len(t, got, 1)
	assert.Equal(t, "enabled-a", got[0].Name)

	t.Run("empty list renders as [] not null", func(t *testing.T) {
		req := testRegionCtx(httptest.NewRequest(http.MethodGet, "/", nil), "vn-south-9")
		rec := httptest.NewRecorder()
		FEListFlavors(store)(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code)
		assert.Equal(t, "[]", jsonCompact(t, rec.Body.Bytes()))
	})
}

func jsonCompact(t *testing.T, b []byte) string {
	t.Helper()
	var v any
	require.NoError(t, json.Unmarshal(b, &v))
	out, err := json.Marshal(v)
	require.NoError(t, err)
	return string(out)
}

func TestAdminFlavorCRUD(t *testing.T) {
	store := &fakeStore{}
	r := mux.NewRouter()
	r.HandleFunc("/apis/v2beta1/admin/flavors", AdminCreateFlavor(store)).Methods(http.MethodPost)
	r.HandleFunc("/apis/v2beta1/admin/flavors/{id}", AdminUpdateFlavor(store)).Methods(http.MethodPut)
	r.HandleFunc("/apis/v2beta1/admin/flavors/{id}", AdminDeleteFlavor(store)).Methods(http.MethodDelete)

	gpu := GPUModelRTX2080
	payload := `{"name":"rtx-basic","region_name":"vn-north-1","description":"d","cpu":4,"memory_gib":16,"gpu_model":"ai_rtx2080","gpu_count":1}`
	req := httptest.NewRequest(http.MethodPost, "/apis/v2beta1/admin/flavors", strings.NewReader(payload))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	require.Equal(t, http.StatusCreated, rec.Code)

	var created Flavor
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &created))
	assert.NotEmpty(t, created.ID, "server assigns the UUID")
	assert.Equal(t, StatusEnabled, created.Status, "default status")
	assert.Equal(t, &gpu, created.GPUModel)

	// Invalid payload: gpu_model without count.
	req = httptest.NewRequest(http.MethodPost, "/apis/v2beta1/admin/flavors",
		strings.NewReader(`{"name":"bad","region_name":"vn-north-1","cpu":1,"memory_gib":1,"gpu_model":"ai_h200","gpu_count":0}`))
	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusBadRequest, rec.Code)

	// Update: disable.
	req = httptest.NewRequest(http.MethodPut, "/apis/v2beta1/admin/flavors/"+created.ID,
		strings.NewReader(`{"name":"rtx-basic","cpu":4,"memory_gib":16,"gpu_model":"ai_rtx2080","gpu_count":1,"status":"disabled"}`))
	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	var updated Flavor
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &updated))
	assert.Equal(t, StatusDisabled, updated.Status)
	assert.Equal(t, "vn-north-1", updated.RegionName, "region immutable")

	// Delete → 204, then 404 on second delete.
	req = httptest.NewRequest(http.MethodDelete, "/apis/v2beta1/admin/flavors/"+created.ID, nil)
	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusNoContent, rec.Code)

	req = httptest.NewRequest(http.MethodDelete, "/apis/v2beta1/admin/flavors/"+created.ID, nil)
	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestWriteStoreError(t *testing.T) {
	rec := httptest.NewRecorder()
	writeStoreError(rec, ErrNotFound)
	assert.Equal(t, http.StatusNotFound, rec.Code)

	rec = httptest.NewRecorder()
	writeStoreError(rec, errors.Join(ErrConflict, errors.New("dup")))
	assert.Equal(t, http.StatusConflict, rec.Code)

	rec = httptest.NewRecorder()
	writeStoreError(rec, errors.New("boom"))
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
}

type stringRd struct{ s string }

/* unused */ func (r *stringRd) Read(p []byte) (int, error) {
	if len(r.s) == 0 {
		return 0, io.EOF
	}
	n := copy(p, r.s)
	r.s = r.s[n:]
	return n, nil
}
