package region

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStripQueryParam(t *testing.T) {
	cases := []struct {
		name, in, key, want string
	}{
		{"empty", "", "region", ""},
		{"only-region", "region=vn-north-1", "region", ""},
		{"region-first", "region=vn-north-1&page_size=5", "region", "page_size=5"},
		{"region-last", "page_size=5&region=vn-north-1", "region", "page_size=5"},
		{"region-middle", "a=1&region=x&b=2", "region", "a=1&b=2"},
		{"encoded-value-untouched", "filter=a%3Db&region=x", "region", "filter=a%3Db"},
		{"no-region", "a=1&b=2", "region", "a=1&b=2"},
		{"empty-region-value", "region=&a=1", "region", "a=1"},
		{"similar-key-kept", "regionx=1&region=2", "region", "regionx=1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, StripQueryParam(tc.in, tc.key))
		})
	}
}

func TestStripTopLevelJSONKey(t *testing.T) {
	t.Run("removes-key-and-preserves-value-bytes", func(t *testing.T) {
		body := []byte(`{"n":1e3,"s":"x","obj":{"region":"nested","a":[1,2]},"region":"vn-north-1"}`)
		newBody, value, state := StripTopLevelJSONKey(body, "region")
		require.Equal(t, JSONKeyRemoved, state)
		assert.Equal(t, "vn-north-1", value)

		// The nested object's bytes are identical, including its region key.
		assert.Contains(t, string(newBody), `"obj":{"region":"nested","a":[1,2]}`)
		// Number literals are not round-tripped through float64.
		assert.Contains(t, string(newBody), `"n":1e3`)
		// The top-level key is gone.
		assert.NotContains(t, string(newBody), `"vn-north-1"`)

		// Result stays a valid JSON object with the remaining fields.
		var obj map[string]json.RawMessage
		require.NoError(t, json.Unmarshal(newBody, &obj))
		assert.Len(t, obj, 3)
	})

	t.Run("key-absent", func(t *testing.T) {
		body := []byte(`{"a":1}`)
		newBody, _, state := StripTopLevelJSONKey(body, "region")
		assert.Equal(t, JSONKeyAbsent, state)
		assert.Equal(t, body, newBody)
	})

	t.Run("not-an-object", func(t *testing.T) {
		for _, body := range []string{`[1,2]`, `"str"`, `123`, `{bad`, `null`} {
			_, _, state := StripTopLevelJSONKey([]byte(body), "region")
			assert.Equal(t, JSONNotObject, state, body)
		}
	})

	t.Run("non-string-value", func(t *testing.T) {
		_, _, state := StripTopLevelJSONKey([]byte(`{"region":{"a":1}}`), "region")
		assert.Equal(t, JSONKeyInvalid, state)
		_, _, state = StripTopLevelJSONKey([]byte(`{"region":null}`), "region")
		assert.Equal(t, JSONKeyInvalid, state)
	})
}
