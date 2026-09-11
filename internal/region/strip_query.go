package region

import "strings"

// StripQueryParam removes every `key` pair from a raw query string while
// preserving the order and encoding of all other pairs. Unlike
// url.Values.Encode(), nothing is re-ordered or re-encoded.
func StripQueryParam(rawQuery, key string) string {
	if rawQuery == "" {
		return ""
	}
	pairs := strings.Split(rawQuery, "&")
	kept := make([]string, 0, len(pairs))
	for _, pair := range pairs {
		k := pair
		if i := strings.IndexByte(pair, '='); i >= 0 {
			k = pair[:i]
		}
		if k == key {
			continue
		}
		kept = append(kept, pair)
	}
	return strings.Join(kept, "&")
}
