package config

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadRegionsFileValid(t *testing.T) {
	regions, err := LoadRegionsFile(filepath.Join("testdata", "regions.valid.json"))
	require.NoError(t, err)
	assert.Len(t, regions, 2)
	assert.Equal(t, "http://10.24.10.26:31132", regions["vn-north-1"].Endpoint)
	assert.True(t, regions["vn-south-2"].InsecureSkipVerify)
}

func TestLoadRegionsFileErrors(t *testing.T) {
	t.Run("bad-url", func(t *testing.T) {
		_, err := LoadRegionsFile(filepath.Join("testdata", "regions.badurl.json"))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "endpoint")
	})
	t.Run("empty", func(t *testing.T) {
		_, err := LoadRegionsFile(filepath.Join("testdata", "regions.empty.json"))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "at least one region")
	})
	t.Run("missing-file", func(t *testing.T) {
		_, err := LoadRegionsFile(filepath.Join("testdata", "nope.json"))
		require.Error(t, err)
	})
}

func TestValidateRegionsNamePattern(t *testing.T) {
	for _, name := range []string{"VN", "vn north", "vn--north", "-vn", "vn-"} {
		err := ValidateRegions(map[string]RegionConfig{name: {Endpoint: "http://x:1"}})
		require.Error(t, err, name)
	}
	for _, name := range []string{"vn-north-1", "vn", "us-east-2"} {
		err := ValidateRegions(map[string]RegionConfig{name: {Endpoint: "http://x:1"}})
		require.NoError(t, err, name)
	}
}

func TestValidateRegionsEndpoint(t *testing.T) {
	for _, ep := range []string{"ftp://x", "10.24.10.26:31132", "", "http://"} {
		err := ValidateRegions(map[string]RegionConfig{"vn-north-1": {Endpoint: ep}})
		require.Error(t, err, ep)
	}
}

func TestStoreLookupAndSwap(t *testing.T) {
	store := NewStore(map[string]RegionConfig{
		"vn-north-1": {Endpoint: "http://a"},
		"vn-south-2": {Endpoint: "http://b"},
	})
	_, ok := store.Lookup("vn-north-1")
	assert.True(t, ok)
	_, ok = store.Lookup(" nope ")
	assert.False(t, ok)
	assert.Equal(t, []string{"vn-north-1", "vn-south-2"}, store.Names())

	// Swap replaces the whole snapshot atomically.
	store.Swap(map[string]RegionConfig{"us-east-1": {Endpoint: "http://c"}})
	_, ok = store.Lookup("vn-north-1")
	assert.False(t, ok)
	_, ok = store.Lookup("us-east-1")
	assert.True(t, ok)
}
