package region

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSetTopLevelJSONKey(t *testing.T) {
	t.Run("sets key on object preserving other values", func(t *testing.T) {
		body := []byte(`{"name":"p1","big":1e3,"nested":{"region":"x"}}`)
		out := SetTopLevelJSONKey(body, "namespace", "proj-pipeline")
		var m map[string]json.RawMessage
		require.NoError(t, json.Unmarshal(out, &m))
		assert.Equal(t, `"proj-pipeline"`, string(m["namespace"]))
		assert.JSONEq(t, `1e3`, string(m["big"]))
		assert.JSONEq(t, `{"region":"x"}`, string(m["nested"]))
	})

	t.Run("overwrites existing key", func(t *testing.T) {
		body := []byte(`{"namespace":"old","a":1}`)
		out := SetTopLevelJSONKey(body, "namespace", "new")
		var m map[string]json.RawMessage
		require.NoError(t, json.Unmarshal(out, &m))
		assert.Equal(t, `"new"`, string(m["namespace"]))
	})

	t.Run("non-object body passes through unchanged", func(t *testing.T) {
		for _, body := range [][]byte{[]byte(`[1,2]`), []byte(`"str"`), []byte(`null`), []byte(`oops`)} {
			assert.Equal(t, body, SetTopLevelJSONKey(body, "k", "v"))
		}
	})
}

func TestSetQueryParam(t *testing.T) {
	tests := []struct {
		name, rawQuery, key, value, want string
	}{
		{"empty query", "", "namespace", "ns", "namespace=ns"},
		{"append keeps others", "a=1&b=two", "namespace", "ns", "a=1&b=two&namespace=ns"},
		{"replaces existing", "namespace=old&a=1", "namespace", "ns", "a=1&namespace=ns"},
		{"replaces all occurrences", "namespace=o1&namespace=o2", "namespace", "ns", "namespace=ns"},
		{"escapes value", "a=1", "namespace", "proj/pipeline", "a=1&namespace=proj%2Fpipeline"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, SetQueryParam(tt.rawQuery, tt.key, tt.value))
		})
	}
}
