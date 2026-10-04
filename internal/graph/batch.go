package graph

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
)

// This file implements /$batch as documented in
// refs/graph/concepts/json-batching.md. The rules that matter:
//
//   - at most 20 requests per batch (:17);
//   - request ids must be unique or the whole batch fails with 400 (:48);
//   - a sub-request url is relative to the service root (:50);
//   - the outer response is "typically 200 or 4xx", and a 200 does NOT mean the
//     sub-requests succeeded (:119) — each item carries its own status and a full
//     error body (:130-239);
//   - sub-request responses can contain their own @odata.nextLink (:142), so a
//     batch item may still need the normal paging loop;
//   - Graph never auto-retries a throttled sub-request, so neither do we (:104-108).

// BatchRequest is one sub-request.
type BatchRequest struct {
	// ID correlates the response; it must be unique within the batch.
	ID string
	// Method is an HTTP method.
	Method string
	// Path is a Graph path relative to the service root ("/me/chats"), or an
	// absolute URL for a next link.
	Path string
	// Query adds query parameters to Path.
	Query map[string]string
	// Headers are copied into the sub-request. Content-Type is added
	// automatically when Body is set, as the docs require (:52).
	Headers map[string]string
	// Body is marshalled as JSON.
	Body any
}

// BatchItemResponse is one sub-response.
type BatchItemResponse struct {
	ID      string            `json:"id"`
	Status  int               `json:"status"`
	Headers map[string]string `json:"headers"`
	// Body is the raw sub-response body; it is JSON for our endpoints.
	Body json.RawMessage `json:"body"`
}

// Decode unmarshals the sub-response body into v.
func (r BatchItemResponse) Decode(v any) error {
	if len(r.Body) == 0 {
		return nil
	}
	if err := json.Unmarshal(r.Body, v); err != nil {
		return fmt.Errorf("decode batch response %q: %w", r.ID, err)
	}
	return nil
}

// Err returns an *APIError when the sub-response is not a success, and nil
// otherwise. The message and code come from the same error envelope a normal
// response would carry.
func (r BatchItemResponse) Err(req BatchRequest) error {
	if r.Status >= 200 && r.Status <= 299 {
		return nil
	}
	url := withQuery(req.Path, req.Query)
	resp := &http.Response{StatusCode: r.Status, Header: http.Header{}}
	for k, v := range r.Headers {
		resp.Header.Set(k, v)
	}
	return parseAPIError(req.Method, url, resp, r.Body)
}

type batchEnvelope struct {
	Requests []batchRequestWire `json:"requests"`
}

type batchRequestWire struct {
	ID      string            `json:"id"`
	Method  string            `json:"method"`
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers,omitempty"`
	Body    json.RawMessage   `json:"body,omitempty"`
}

type batchResponseEnvelope struct {
	Responses []BatchItemResponse `json:"responses"`
}

// Batch sends the sub-requests in one call and returns the responses keyed by
// id: the documented order is not guaranteed, so callers must not rely on it.
//
// A sub-request failure is not an error here — it is reported per item, because
// that is how Graph reports it. Callers check BatchItemResponse.Err and can
// retry a throttled item themselves, using the retry-after value from its own
// response.
func (c *Client) Batch(ctx context.Context, reqs []BatchRequest) (map[string]BatchItemResponse, error) {
	if len(reqs) == 0 {
		return nil, errors.New("graph: batch needs at least one request")
	}
	if len(reqs) > MaxBatchRequests {
		return nil, fmt.Errorf("graph: %d sub-requests exceeds the documented maximum of %d", len(reqs), MaxBatchRequests)
	}
	wire := batchEnvelope{Requests: make([]batchRequestWire, 0, len(reqs))}
	seen := make(map[string]bool, len(reqs))
	for _, req := range reqs {
		if strings.TrimSpace(req.ID) == "" {
			return nil, errors.New("graph: every batch request needs an id")
		}
		if seen[req.ID] {
			return nil, fmt.Errorf("graph: duplicate batch request id %q (Graph answers 400 for that)", req.ID)
		}
		seen[req.ID] = true
		if req.Method == "" || req.Path == "" {
			return nil, fmt.Errorf("graph: batch request %q needs a method and a url", req.ID)
		}
		item := batchRequestWire{
			ID:      req.ID,
			Method:  req.Method,
			URL:     withQuery(req.Path, req.Query),
			Headers: req.Headers,
		}
		if req.Body != nil {
			raw, err := json.Marshal(req.Body)
			if err != nil {
				return nil, fmt.Errorf("graph: encode batch request %q: %w", req.ID, err)
			}
			item.Body = raw
			if item.Headers == nil {
				item.Headers = map[string]string{}
			}
			if _, ok := item.Headers["Content-Type"]; !ok {
				item.Headers["Content-Type"] = "application/json"
			}
		}
		wire.Requests = append(wire.Requests, item)
	}

	resp, err := c.Do(ctx, Request{Method: http.MethodPost, Path: "/$batch", Body: wire})
	if err != nil {
		return nil, err
	}
	var envelope batchResponseEnvelope
	if err := resp.Decode(&envelope); err != nil {
		return nil, err
	}
	out := make(map[string]BatchItemResponse, len(envelope.Responses))
	for _, item := range envelope.Responses {
		out[item.ID] = item
	}
	for id := range seen {
		if _, ok := out[id]; !ok {
			return out, fmt.Errorf("graph: batch response is missing sub-response %q", id)
		}
	}
	return out, nil
}

// BatchOrdered is Batch plus a slice in the request order, for callers that
// want stable output.
func (c *Client) BatchOrdered(ctx context.Context, reqs []BatchRequest) ([]BatchItemResponse, error) {
	byID, err := c.Batch(ctx, reqs)
	out := make([]BatchItemResponse, 0, len(reqs))
	for _, req := range reqs {
		if item, ok := byID[req.ID]; ok {
			out = append(out, item)
		}
	}
	return out, err
}

func withQuery(path string, query map[string]string) string {
	if len(query) == 0 {
		return path
	}
	parts := make([]string, 0, len(query))
	for k, v := range query {
		parts = append(parts, k+"="+v)
	}
	sep := "?"
	if strings.Contains(path, "?") {
		sep = "&"
	}
	return path + sep + strings.Join(parts, "&")
}
