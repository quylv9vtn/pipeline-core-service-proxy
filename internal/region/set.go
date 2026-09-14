package region

import (
	"encoding/json"
	"net/url"
)

// SetTopLevelJSONKey sets (or overwrites) a top-level string key on a JSON
// object body. It is the write counterpart of StripTopLevelJSONKey and
// follows the same semantics: non-object bodies pass through unchanged
// (the upstream expresses the error), and RawMessage keeps every other
// value byte-exact.
func SetTopLevelJSONKey(body []byte, key, value string) []byte {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(body, &obj); err != nil || obj == nil {
		return body
	}
	obj[key], _ = json.Marshal(value)
	out, err := json.Marshal(obj)
	if err != nil {
		return body
	}
	return out
}

// SetQueryParam appends key=value to a raw query string, replacing any
// existing pair for that key. Other pairs keep their order and encoding.
func SetQueryParam(rawQuery, key, value string) string {
	stripped := StripQueryParam(rawQuery, key)
	pair := key + "=" + url.QueryEscape(value)
	if stripped == "" {
		return pair
	}
	return stripped + "&" + pair
}
