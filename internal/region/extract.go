package region

import (
	"bytes"
	"context"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"

	"github.com/viettel-ai-platform/pipeline-core-service/internal/config"
)

type ctxKey struct{}

// Result is the resolved region for a request.
type Result struct {
	Name   string
	Source Source
}

// IntoContext attaches the resolved region to the request context.
func IntoContext(ctx context.Context, res *Result) context.Context {
	return context.WithValue(ctx, ctxKey{}, res)
}

// FromContext returns the resolved region, if any.
func FromContext(ctx context.Context) (*Result, bool) {
	res, ok := ctx.Value(ctxKey{}).(*Result)
	return res, ok
}

// Process performs the strict region pass over r, mutating it in place:
// extraction (X-Region header > ?region= query > top-level "region" JSON
// body field), validation against the store, and stripping of the query
// parameter and body field so the upstream sees a request it accepts
// (the apiserver unmarshals JSON with DiscardUnknown: false and would
// reject an unknown "region" field itself).
//
// On success the request body/query are ready for forwarding. On error the
// request is left forwardable (original bytes restored) but callers write
// the APIError instead of proxying.
func Process(r *http.Request, store *config.Store, maxJSONBodyBytes int64) (*Result, *APIError) {
	valid := store.Names()

	// 1. X-Region header.
	if v := strings.TrimSpace(r.Header.Get(HeaderName)); v != "" {
		res, apiErr := validate(v, SourceHeader, valid, store)
		if apiErr != nil {
			return nil, apiErr
		}
		return finish(r, res), nil
	}

	// 2. ?region= query parameter (first non-empty value).
	for _, qv := range r.URL.Query()[QueryParam] {
		if v := strings.TrimSpace(qv); v != "" {
			res, apiErr := validate(v, SourceQuery, valid, store)
			if apiErr != nil {
				return nil, apiErr
			}
			return finish(r, res), nil
		}
	}

	// 3. Top-level "region" JSON body field.
	if hasBody(r) {
		mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err == nil {
			switch {
			case strings.HasPrefix(mediaType, "multipart/"):
				// The gateway never parses multipart bodies.
				return nil, multipartRegion()
			case mediaType == "application/json":
				return processJSONBody(r, store, valid, maxJSONBodyBytes)
			}
		}
	}

	return nil, missingRegion(valid)
}

func validate(name string, src Source, valid []string, store *config.Store) (*Result, *APIError) {
	if _, ok := store.Lookup(name); !ok {
		return nil, UnknownRegion(name, valid)
	}
	return &Result{Name: name, Source: src}, nil
}

// finish strips the region query pair on every success path so a stale or
// empty ?region= never reaches the upstream.
func finish(r *http.Request, res *Result) *Result {
	r.URL.RawQuery = StripQueryParam(r.URL.RawQuery, QueryParam)
	return res
}

func processJSONBody(r *http.Request, store *config.Store, valid []string, maxBytes int64) (*Result, *APIError) {
	buf, tooBig, err := readBounded(r.Body, maxBytes)
	if err != nil {
		return nil, &APIError{
			Status:  http.StatusBadRequest,
			Code:    "invalid_request_body",
			Message: "Failed to read request body: " + err.Error(),
		}
	}
	if tooBig {
		return nil, payloadTooLarge()
	}

	newBody, value, state := StripTopLevelJSONKey(buf, JSONField)
	switch state {
	case JSONNotObject, JSONKeyAbsent:
		replaceBody(r, buf) // forward the original bytes unchanged
		return nil, missingRegion(valid)
	case JSONKeyInvalid:
		return nil, invalidRegionField()
	}

	name := strings.TrimSpace(value)
	if name == "" {
		// An empty region is treated as missing; forward the original body.
		replaceBody(r, buf)
		return nil, missingRegion(valid)
	}

	res, apiErr := validate(name, SourceBody, valid, store)
	if apiErr != nil {
		replaceBody(r, buf)
		return nil, apiErr
	}
	replaceBody(r, newBody)
	return finish(r, res), nil
}

// hasBody reports whether the request plausibly carries a body
// (ContentLength 0 means none; -1 means chunked/unknown).
func hasBody(r *http.Request) bool {
	return r.Body != nil && r.ContentLength != 0
}

func readBounded(rc io.ReadCloser, limit int64) (buf []byte, tooBig bool, err error) {
	buf, err = io.ReadAll(io.LimitReader(rc, limit+1))
	if err != nil {
		return nil, false, err
	}
	if int64(len(buf)) > limit {
		return nil, true, nil
	}
	return buf, false, nil
}

// replaceBody swaps in a fully-buffered body and fixes the length metadata
// so the reverse proxy forwards a consistent request. TransferEncoding must
// be cleared: a chunked inbound request would otherwise carry the
// ["chunked"] token through the proxy clone and force the outbound
// transport to re-chunk, ignoring the recomputed ContentLength.
func replaceBody(r *http.Request, body []byte) {
	r.Body = io.NopCloser(bytes.NewReader(body))
	r.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(body)), nil }
	r.ContentLength = int64(len(body))
	r.TransferEncoding = nil
	r.Header.Set("Content-Length", strconv.Itoa(len(body)))
	r.Header.Del("Transfer-Encoding")
}
