package fakegraph

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/floriscornel/teams-cli/internal/graph"
)

// This file covers /$batch: the documented maximum of 20, unique ids, the rule
// that sub-request failures travel inside a 200 outer response with a full
// error envelope, and a sub-response that carries its own @odata.nextLink
// (refs/graph/concepts/json-batching.md:17,48,119,142).

// rawPost sends a raw JSON body to a path and returns the status and body.
func rawPost(t *testing.T, srv *Server, path string, body any) (int, []byte) {
	t.Helper()
	payload, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodPost, srv.URL()+path, strings.NewReader(string(payload)))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("POST %s: %v", path, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, raw
}

func batchItem(id, method, url string, body any, headers map[string]string) map[string]any {
	item := map[string]any{"id": id, "method": method, "url": url}
	if body != nil {
		item["body"] = body
	}
	if headers != nil {
		item["headers"] = headers
	}
	return item
}

func TestBatchMaximumIs20(t *testing.T) {
	srv := newTestServer(t)

	ok := make([]any, 0, maxBatchRequests)
	for i := 0; i < maxBatchRequests; i++ {
		ok = append(ok, batchItem(itoa(i), "GET", "/me", nil, nil))
	}
	status, body := rawPost(t, srv, "/$batch", map[string]any{"requests": ok})
	if status != 200 {
		t.Fatalf("20 requests: status = %d, body %s", status, body)
	}

	tooMany := append(append([]any(nil), ok...), batchItem("extra", "GET", "/me", nil, nil))
	status, body = rawPost(t, srv, "/$batch", map[string]any{"requests": tooMany})
	if status != 400 {
		t.Fatalf("21 requests: status = %d, want 400 (the whole batch must fail)", status)
	}
	if !strings.Contains(string(body), "maximum of 20") {
		t.Fatalf("21 requests: body %s, want it to name the limit", body)
	}

	status, _ = rawPost(t, srv, "/$batch", map[string]any{"requests": []any{}})
	if status != 400 {
		t.Fatalf("empty batch: status = %d, want 400", status)
	}
}

func TestBatchRejectsDuplicateIDs(t *testing.T) {
	srv := newTestServer(t)
	// Ids are documented as "Not case-sensitive", so 1 and 1 collide.
	status, body := rawPost(t, srv, "/$batch", map[string]any{"requests": []any{
		batchItem("1", "GET", "/me", nil, nil),
		batchItem("1", "GET", "/me/chats", nil, nil),
	}})
	if status != 400 {
		t.Fatalf("status = %d, want 400; body %s", status, body)
	}
	if !strings.Contains(string(body), "not unique") {
		t.Fatalf("body %s, want a duplicate-id error", body)
	}
}

func TestBatchShapeValidation(t *testing.T) {
	srv := newTestServer(t)

	// A batch that cannot be parsed fails as a whole with 400
	// (refs/graph/concepts/json-batching.md:119).
	batchLevel := []struct {
		name  string
		items []any
	}{
		{"missing id", []any{map[string]any{"method": "GET", "url": "/me"}}},
		{"missing method", []any{map[string]any{"id": "1", "url": "/me"}}},
		{"missing url", []any{map[string]any{"id": "1", "method": "GET"}}},
		{"nested batch", []any{batchItem("1", "POST", "/$batch", nil, map[string]string{"Content-Type": "application/json"})}},
	}
	for _, tc := range batchLevel {
		t.Run(tc.name, func(t *testing.T) {
			status, body := rawPost(t, srv, "/$batch", map[string]any{"requests": tc.items})
			if status != 400 {
				t.Fatalf("status = %d, want 400; body %s", status, body)
			}
		})
	}

	// A per-request problem is reported inside the 200 envelope, because the
	// batch itself parsed.
	itemLevel := []struct {
		name  string
		items []any
	}{
		{"body without content type", []any{batchItem("1", "POST", "/chats/chat-group/messages", map[string]any{"body": map[string]string{"content": "hi"}}, nil)}},
		{"dependsOn unknown id", []any{map[string]any{"id": "1", "method": "GET", "url": "/me", "dependsOn": []string{"nope"}}}},
	}
	for _, tc := range itemLevel {
		t.Run(tc.name, func(t *testing.T) {
			status, body := rawPost(t, srv, "/$batch", map[string]any{"requests": tc.items})
			if status != 200 {
				t.Fatalf("outer status = %d, want 200 (a parseable batch); body %s", status, body)
			}
			var env batchResponseEnvelopeWire
			if err := json.Unmarshal(body, &env); err != nil {
				t.Fatal(err)
			}
			if len(env.Responses) != 1 || env.Responses[0].Status != 400 {
				t.Fatalf("item statuses = %+v, want one 400", env.Responses)
			}
			if len(env.Responses[0].Body) == 0 {
				t.Fatal("the failed item carried no error envelope")
			}
		})
	}
}

func TestBatchFailureTravelsInsideA200(t *testing.T) {
	// The scope set is deliberately narrow, so the second sub-request is
	// refused while the first succeeds: a 403 inside a 200 outer response is
	// the documented shape (json-batching.md:119-239).
	srv := New(t, Options{
		Model:  testModel(),
		Clock:  newFakeClock(),
		Scopes: []string{"Chat.Read", "User.Read", "Team.ReadBasic.All"},
	})
	c := newClient(t, srv)

	items, err := c.Batch(context.Background(), []graph.BatchRequest{
		{ID: "read", Method: http.MethodGet, Path: "/me"},
		{ID: "write", Method: http.MethodPost, Path: "/chats/chat-1on1/messages", Body: map[string]any{
			"body": map[string]string{"content": "<p>hi</p>"},
		}},
		{ID: "missing", Method: http.MethodGet, Path: "/teams/nope"},
	})
	if err != nil {
		t.Fatalf("Batch returned a transport error: %v", err)
	}
	if got := items["read"].Status; got != 200 {
		t.Fatalf("read status = %d, want 200", got)
	}
	write := items["write"]
	if write.Status != 403 {
		t.Fatalf("write status = %d, want 403; body %s", write.Status, write.Body)
	}
	var env errorEnvelopeWire
	if err := json.Unmarshal(write.Body, &env); err != nil {
		t.Fatalf("403 sub-response body is not a Graph error envelope: %v (%s)", err, write.Body)
	}
	if env.Error.Code != "Authorization_RequestDenied" || !strings.Contains(env.Error.Message, "ChatMessage.Send") {
		t.Fatalf("403 envelope = %+v", env.Error)
	}
	if err := write.Err(graph.BatchRequest{ID: "write", Method: http.MethodPost, Path: "/chats/chat-1on1/messages"}); err == nil {
		t.Fatal("BatchItemResponse.Err returned nil for a 403")
	}
	missing := items["missing"]
	if missing.Status != 404 {
		t.Fatalf("missing status = %d, want 404; body %s", missing.Status, missing.Body)
	}
	if len(missing.Body) == 0 {
		t.Fatal("a failed sub-response carried no error body")
	}
}

func TestBatchSubResponseCarriesNextLink(t *testing.T) {
	srv := newBigServer(t)
	c := newClient(t, srv)

	items, err := c.Batch(context.Background(), []graph.BatchRequest{
		{ID: "page1", Method: http.MethodGet, Path: "/teams/t1/channels/c1/messages", Query: map[string]string{"$top": "5"}},
	})
	if err != nil {
		t.Fatalf("Batch: %v", err)
	}
	item := items["page1"]
	if item.Status != 200 {
		t.Fatalf("status = %d", item.Status)
	}
	var page struct {
		Value    []json.RawMessage `json:"value"`
		NextLink string            `json:"@odata.nextLink"`
	}
	if err := item.Decode(&page); err != nil {
		t.Fatalf("decode sub-response: %v", err)
	}
	if len(page.Value) != 5 {
		t.Fatalf("sub-response page size = %d, want 5", len(page.Value))
	}
	if page.NextLink == "" {
		t.Fatal("a sub-response was not allowed to carry its own @odata.nextLink")
	}
	// The link is absolute and follows a normal GET.
	next := mustPage[messageWire](t, c, page.NextLink, nil)
	if len(next.Value) != 5 {
		t.Fatalf("followed next link returned %d items", len(next.Value))
	}
}

func TestBatchDependsOnFailureIs424(t *testing.T) {
	srv := newTestServer(t)
	status, body := rawPost(t, srv, "/$batch", map[string]any{"requests": []any{
		batchItem("first", "GET", "/teams/nope", nil, nil),
		map[string]any{"id": "second", "method": "GET", "url": "/me", "dependsOn": []string{"first"}},
	}})
	if status != 200 {
		t.Fatalf("outer status = %d, want 200; body %s", status, body)
	}
	var env batchResponseEnvelopeWire
	if err := json.Unmarshal(body, &env); err != nil {
		t.Fatal(err)
	}
	byID := map[string]batchResponseItemWire{}
	for _, item := range env.Responses {
		byID[item.ID] = item
	}
	if byID["first"].Status != 404 {
		t.Fatalf("dependency status = %d, want 404", byID["first"].Status)
	}
	if byID["second"].Status != 424 {
		t.Fatalf("dependent status = %d, want 424 Failed Dependency", byID["second"].Status)
	}
}

func TestBatchSubRequestsAreRecorded(t *testing.T) {
	srv := newTestServer(t)
	c := newClient(t, srv)
	if _, err := c.Batch(context.Background(), []graph.BatchRequest{
		{ID: "1", Method: http.MethodGet, Path: "/me"},
	}); err != nil {
		t.Fatalf("Batch: %v", err)
	}
	var outer, sub int
	for _, rec := range srv.Requests() {
		if rec.Path != "/$batch" && rec.Path != "/me" {
			continue
		}
		switch {
		case rec.Path == "/$batch":
			outer++
			if rec.Sub {
				t.Error("the outer $batch request was marked as a sub-request")
			}
		case rec.Path == "/me":
			sub++
			if !rec.Sub {
				t.Error("a $batch sub-request was not marked as such")
			}
		}
	}
	if outer != 1 || sub != 1 {
		t.Fatalf("recorded outer=%d sub=%d, want 1 and 1", outer, sub)
	}
}

func TestBatchEnvelopeMatchesLocalSchema(t *testing.T) {
	srv := newTestServer(t)
	c := newClient(t, srv)
	if _, err := c.Batch(context.Background(), []graph.BatchRequest{
		{ID: "1", Method: http.MethodGet, Path: "/me"},
		{ID: "2", Method: http.MethodPost, Path: "/chats/chat-group/messages", Body: map[string]any{"body": map[string]string{"content": "hello"}}},
	}); err != nil {
		t.Fatalf("Batch: %v", err)
	}
	rec := srv.RequestsFor(http.MethodPost, "/$batch")
	if len(rec) != 1 {
		t.Fatalf("recorded %d $batch requests, want 1", len(rec))
	}
	// The Graph OpenAPI description has no batch path or schema, so the local
	// schema is the only contract $batch has (PLAN.md Layer 6).
	if err := BatchContract(rec[0].Body); err != nil {
		t.Fatalf("ValidateBatchEnvelope: %v\nbody: %s", err, rec[0].Body)
	}
	// A deliberately broken envelope must fail the local schema.
	if err := BatchContract([]byte(`{"requests":[{"id":"1"}]}`)); err == nil {
		t.Fatal("ValidateBatchEnvelope accepted a request without method/url")
	}
}
