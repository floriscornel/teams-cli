package fakegraph

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"time"
)

// This file implements /$batch exactly as
// refs/graph/concepts/json-batching.md documents it:
//
//   - at most 20 requests in one batch, or the batch fails with 400 (:17,:296);
//   - request ids must be unique or the whole batch fails with 400 (:48);
//   - the outer response is 200 whenever the batch itself parses, even when
//     every sub-request failed (:119);
//   - a sub-response carries its own status and a full error envelope
//     (:119-239);
//   - a sub-response may contain its own @odata.nextLink (:142);
//   - a request whose dependsOn target failed answers 424 (:285).
//
// Graph never auto-retries a throttled sub-request (:298), so neither does the
// fake: the item comes back as a 429 and the caller decides.

// maxBatchRequests mirrors the documented limit and internal/graph's
// MaxBatchRequests (refs/graph/concepts/json-batching.md:17).
const maxBatchRequests = 20

// handleBatch serves POST /$batch.
func handleBatch(c *handlerCtx) {
	var env batchRequestEnvelopeWire
	if !c.decodeBody(&env) {
		return
	}
	if len(env.Requests) == 0 {
		c.fail(badRequestf("The batch request must contain at least one request."))
		return
	}
	if len(env.Requests) > maxBatchRequests {
		c.fail(badRequestf("The batch request exceeds the maximum of %d requests (got %d).", maxBatchRequests, len(env.Requests)))
		return
	}
	seen := map[string]bool{}
	for _, item := range env.Requests {
		if strings.TrimSpace(item.ID) == "" {
			c.fail(badRequestf("Every batch request needs an id."))
			return
		}
		key := strings.ToLower(item.ID)
		if seen[key] {
			c.fail(badRequestf("The batch request id %q is not unique.", item.ID))
			return
		}
		seen[key] = true
		if item.Method == "" || item.URL == "" {
			c.fail(badRequestf("The batch request %q needs a method and a url.", item.ID))
			return
		}
		if isBatchPath(item.URL) {
			// Graph does not support nesting $batch inside $batch.
			c.fail(badRequestf("A batch request cannot contain another $batch request."))
			return
		}
	}

	results := map[string]batchResponseItemWire{}
	visiting := map[string]bool{}
	var run func(item batchRequestItemWire, depth int) batchResponseItemWire
	run = func(item batchRequestItemWire, depth int) batchResponseItemWire {
		key := strings.ToLower(item.ID)
		if existing, ok := results[key]; ok {
			return existing
		}
		if visiting[key] || depth > len(env.Requests) {
			return errorItem(item.ID, http.StatusBadRequest, "BadRequest", "The batch request has a cyclic dependsOn chain.")
		}
		visiting[key] = true
		defer delete(visiting, key)
		for _, dep := range item.DependsOn {
			depItem, ok := findBatchItem(env.Requests, dep)
			if !ok {
				results[key] = errorItem(item.ID, http.StatusBadRequest, "BadRequest", "dependsOn references the unknown request id "+dep+".")
				return results[key]
			}
			depResult := run(depItem, depth+1)
			if depResult.Status >= 400 {
				// "If an individual request fails, any request that depends on
				// that request fails with status code 424" (json-batching.md:285).
				results[key] = errorItem(item.ID, http.StatusFailedDependency, "FailedDependency", "A request this one depends on failed.")
				return results[key]
			}
		}
		results[key] = c.s.execSubRequest(c, item)
		return results[key]
	}

	out := make([]batchResponseItemWire, 0, len(env.Requests))
	for _, item := range env.Requests {
		out = append(out, run(item, 0))
	}
	// The documented outer status: "If the batch request is parseable, the
	// status code is 200" (json-batching.md:119).
	c.json(http.StatusOK, batchResponseEnvelopeWire{Responses: out})
}

// execSubRequest runs one sub-request through the same dispatch path as a
// top-level call, so its route scopes, query limits and error shapes are
// identical. The sub-request is recorded with Sub set.
func (s *Server) execSubRequest(parent *handlerCtx, item batchRequestItemWire) batchResponseItemWire {
	rel, err := s.relativeURL(item.URL)
	if err != nil {
		return errorItem(item.ID, http.StatusBadRequest, "BadRequest", err.Error())
	}
	method := strings.ToUpper(item.Method)
	var body []byte
	if len(item.Body) > 0 && !bytes.Equal(bytes.TrimSpace(item.Body), []byte("null")) {
		body = item.Body
	}
	req := httptest.NewRequestWithContext(parent.r.Context(), method, s.origin+rel, bytes.NewReader(body))
	for k, v := range item.Headers {
		req.Header.Set(k, v)
	}
	if body != nil && req.Header.Get("Content-Type") == "" {
		// "When the body is supplied, a Content-Type header must be included"
		// (refs/graph/concepts/json-batching.md:53).
		return errorItem(item.ID, http.StatusBadRequest, "BadRequest", "A batch request with a body must include a Content-Type header.")
	}
	if auth := parent.r.Header.Get("Authorization"); auth != "" {
		req.Header.Set("Authorization", auth)
	}
	if prefer := parent.r.Header.Get("Prefer"); prefer != "" {
		req.Header.Set("Prefer", prefer)
	}
	if rid := parent.r.Header.Get("client-request-id"); rid != "" {
		req.Header.Set("client-request-id", rid)
	}

	rec := httptest.NewRecorder()
	s.serve(rec, req, true)

	result := batchResponseItemWire{ID: item.ID, Status: rec.Code}
	headers := map[string]string{}
	if ct := rec.Header().Get("Content-Type"); ct != "" {
		headers["Content-Type"] = ct
	}
	if ra := rec.Header().Get("Retry-After"); ra != "" {
		headers["Retry-After"] = ra
	}
	if len(headers) > 0 {
		result.Headers = headers
	}
	if rec.Body.Len() > 0 {
		result.Body = append(json.RawMessage(nil), rec.Body.Bytes()...)
	}
	return result
}

// relativeURL normalizes a sub-request URL to a path under the service root.
// The docs say the url is relative to the service root ("/users"), and a
// sub-response may carry an absolute @odata.nextLink, which a batch can follow
// (refs/graph/concepts/json-batching.md:50,142).
func (s *Server) relativeURL(raw string) (string, error) {
	if strings.HasPrefix(raw, "http://") || strings.HasPrefix(raw, "https://") {
		u, err := url.Parse(raw)
		if err != nil {
			return "", fmt.Errorf("the batch sub-request URL %q is not a valid URL", raw)
		}
		base, err := url.Parse(s.origin)
		if err != nil || !strings.EqualFold(u.Host, base.Host) {
			return "", fmt.Errorf("the batch sub-request URL %q points at another host", raw)
		}
		raw = u.Path
		if u.RawQuery != "" {
			raw += "?" + u.RawQuery
		}
	}
	if !strings.HasPrefix(raw, "/") {
		raw = "/" + raw
	}
	if strings.HasPrefix(raw, s.opts.BasePath+"/") || raw == s.opts.BasePath {
		return raw, nil
	}
	return s.opts.BasePath + raw, nil
}

// errorItem builds a failed sub-response with a full Graph error envelope,
// which is what a batch item carries on failure
// (refs/graph/concepts/json-batching.md:130-239).
func errorItem(id string, status int, code, message string) batchResponseItemWire {
	body, _ := json.Marshal(errorEnvelopeWire{Error: errorBodyWire{
		Code:    code,
		Message: message,
		InnerError: &innerErrorWire{
			Date:   graphTime(time.Now().UTC()),
			Status: itoa(status),
		},
	}})
	return batchResponseItemWire{
		ID:      id,
		Status:  status,
		Headers: map[string]string{"Content-Type": "application/json"},
		Body:    body,
	}
}

// findBatchItem looks up a dependsOn target case-insensitively, because the
// docs say an id is "Not case-sensitive" (json-batching.md:48).
func findBatchItem(items []batchRequestItemWire, id string) (batchRequestItemWire, bool) {
	for _, item := range items {
		if strings.EqualFold(item.ID, id) {
			return item, true
		}
	}
	return batchRequestItemWire{}, false
}

// isBatchPath reports whether a sub-request targets /$batch itself.
func isBatchPath(raw string) bool {
	path := raw
	if i := strings.IndexByte(path, '?'); i >= 0 {
		path = path[:i]
	}
	return strings.HasSuffix(strings.TrimSuffix(path, "/"), "/$batch")
}
