package region

import "encoding/json"

// JSONKeyState classifies the outcome of a top-level key extraction.
type JSONKeyState int

const (
	// JSONNotObject: body is not valid JSON or is not an object.
	// Callers forward the original body unchanged (the upstream expresses
	// the error better than we could).
	JSONNotObject JSONKeyState = iota
	// JSONKeyAbsent: JSON object without the key. Forward unchanged.
	JSONKeyAbsent
	// JSONKeyRemoved: key existed, held a JSON string, and was removed.
	// Use the returned newBody. Value holds the (untrimmed) string.
	JSONKeyRemoved
	// JSONKeyInvalid: key existed but was not a JSON string.
	JSONKeyInvalid
)

// StripTopLevelJSONKey removes a top-level key from a JSON object body,
// preserving the exact bytes of every other value (json.RawMessage keeps
// number literals like 1e3 and nested objects — including nested "region"
// keys — untouched). Top-level key order is normalized (maps marshal
// sorted), which is semantically lossless.
func StripTopLevelJSONKey(body []byte, key string) (newBody []byte, value string, state JSONKeyState) {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(body, &obj); err != nil || obj == nil {
		// obj == nil covers the JSON literal null (valid JSON, no object).
		return body, "", JSONNotObject
	}
	raw, ok := obj[key]
	if !ok {
		return body, "", JSONKeyAbsent
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil || string(raw) == "null" {
		// Non-string value, or the JSON literal null (which unmarshals
		// into a string as a silent no-op but protojson would reject).
		return body, "", JSONKeyInvalid
	}
	delete(obj, key)
	out, err := json.Marshal(obj)
	if err != nil {
		// Cannot happen for a successfully unmarshaled map, but stay safe:
		// forward the original rather than half-processed bytes.
		return body, "", JSONNotObject
	}
	return out, s, JSONKeyRemoved
}
