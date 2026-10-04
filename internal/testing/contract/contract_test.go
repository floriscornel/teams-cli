package contract

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// repoRoot returns the repository root, which is three levels up from this
// package (internal/testing/contract).
func repoRoot() string {
	dir, err := os.Getwd()
	if err != nil {
		panic(err)
	}
	return filepath.Clean(filepath.Join(dir, "..", "..", ".."))
}

// hasRefs reports whether the gitignored OpenAPI mirror is present, so the
// currency test can skip instead of failing on a machine that never ran
// scripts/fetch-refs.sh (PLAN.md: "Tests never need refs/").
func hasRefs(t *testing.T) string {
	t.Helper()
	root := repoRoot()
	if _, err := os.Stat(filepath.Join(root, openAPISource)); err != nil {
		t.Skipf("skipping: %s is not present; run scripts/fetch-refs.sh", openAPISource)
	}
	return root
}

func TestLoadEmbeddedSpec(t *testing.T) {
	v := Load(t)
	if v == nil || v.doc == nil {
		t.Fatal("Load returned no document")
	}
	if v.doc.Info == nil || v.doc.Info.Title == "" {
		t.Error("trimmed spec has no info.title")
	}
	if got := len(Routes()); got < 40 {
		t.Errorf("route list has %d entries, expected the committed surface", got)
	}
}

// TestRoutesAllResolve checks that every committed route really resolves against
// the trimmed spec, i.e. that the generator kept exactly the paths we use. A
// route in routes.txt that has no matching path template would silently make
// every contract test for that command vacuous.
func TestRoutesAllResolve(t *testing.T) {
	v := Load(t)
	for _, line := range Routes() {
		method, path, ok := strings.Cut(line, " ")
		if !ok {
			t.Fatalf("malformed route %q", line)
		}
		if _, _, err := v.findRoute(method, concretePath(path)); err != nil {
			t.Errorf("route %s does not resolve against the trimmed spec: %v", line, err)
		}
	}
}

// concretePath replaces every {parameter} with a plausible value so a committed
// template can be looked up as a concrete request path.
func concretePath(template string) string {
	var b strings.Builder
	for {
		i := strings.Index(template, "{")
		if i < 0 {
			b.WriteString(template)
			break
		}
		j := strings.Index(template[i:], "}")
		b.WriteString(template[:i])
		b.WriteString("00000000-0000-0000-0000-000000000000")
		template = template[i+j+1:]
	}
	return b.String()
}

// TestValidateRequestChannelMessage is the case PLAN.md calls out: a correct
// channel message POST with body{content,contentType} and no @odata.type must
// pass.
func TestValidateRequestChannelMessage(t *testing.T) {
	v := Load(t)
	path := "/teams/5e3ce6c0-2b1f-4285-8d4b-75ee78787346/channels/19:abc@thread.tacv2/messages"
	body := []byte(`{"body":{"content":"<p>hello</p>","contentType":"html"}}`)
	if err := v.ValidateRequest("POST", path, nil, body); err != nil {
		t.Fatalf("a correct channel message POST failed: %v", err)
	}
}

// TestValidateRequestChatMessage covers the chat container, which uses a
// different operation and a different set of scopes.
func TestValidateRequestChatMessage(t *testing.T) {
	v := Load(t)
	path := "/chats/19:82fe7758-5bb3-4f0d-a43f-e555fd399c6f_bfb5bb25@unq.gbl.spaces/messages"
	body := []byte(`{"body":{"content":"hi","contentType":"text"}}`)
	if err := v.ValidateRequest("POST", path, nil, body); err != nil {
		t.Fatalf("a correct chat message POST failed: %v", err)
	}
}

// TestValidateRequestSearchQuery covers POST /search/query, whose request body
// schema the trimmed spec does carry (so no exemption is needed for it).
func TestValidateRequestSearchQuery(t *testing.T) {
	v := Load(t)
	body := []byte(`{"requests":[{"entityTypes":["chatMessage"],"query":{"queryString":"from:alice"},"from":0,"size":25}]}`)
	if err := v.ValidateRequest("POST", "/search/query", nil, body); err != nil {
		t.Fatalf("a correct /search/query POST failed: %v", err)
	}

	// A second page of results is the documented from/size protocol.
	if err := v.ValidateRequest("POST", "/search/query", nil, []byte(
		`{"requests":[{"entityTypes":["chatMessage"],"query":{"queryString":"hi"},"from":25,"size":25}]}`)); err != nil {
		t.Fatalf("a paged /search/query POST failed: %v", err)
	}

	// A structurally wrong envelope must be rejected.
	if err := v.ValidateRequest("POST", "/search/query", nil, []byte(`{"requests":"not-an-array"}`)); err == nil {
		t.Fatal("a /search/query body with requests as a string was accepted")
	}
}

// TestValidateRequestQueryParameters covers the query half of the check.
func TestValidateRequestQueryParameters(t *testing.T) {
	v := Load(t)
	path := "/teams/5e3ce6c0-2b1f-4285-8d4b-75ee78787346/channels/19:abc@thread.tacv2/messages"
	q := url.Values{"$top": {"50"}, "$filter": {"lastModifiedDateTime gt 2024-01-01T00:00:00Z"}}
	if err := v.ValidateRequest("GET", path, q, nil); err != nil {
		t.Fatalf("a correct paged GET failed: %v", err)
	}

	bad := url.Values{"$top": {"not-a-number"}}
	err := v.ValidateRequest("GET", path, bad, nil)
	if err == nil {
		t.Fatal("a non-integer $top was accepted")
	}
	var ce *Error
	if !errors.As(err, &ce) {
		t.Fatalf("error is %T, want *Error", err)
	}
	if ce.Part != PartQuery {
		t.Errorf("part = %q, want %q", ce.Part, PartQuery)
	}
	if !strings.Contains(ce.Value, "not-a-number") {
		t.Errorf("error does not echo the offending value: %q", ce.Value)
	}
}

// TestValidateRequestRejectsWrongType pins the actionable error shape: route,
// part, and the offending JSON location and value.
func TestValidateRequestRejectsWrongType(t *testing.T) {
	v := Load(t)
	path := "/teams/5e3ce6c0-2b1f-4285-8d4b-75ee78787346/channels/19:abc@thread.tacv2/messages"
	err := v.ValidateRequest("POST", path, nil, []byte(`{"body":{"content":42}}`))
	if err == nil {
		t.Fatal("a numeric body.content was accepted")
	}
	msg := err.Error()
	for _, want := range []string{path, "body", "/body/content", "42"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error message %q does not mention %q", msg, want)
		}
	}
}

// TestValidateRequestUnknownPath reports a path that the trimmed spec does not
// have, rather than a confusing schema error.
func TestValidateRequestUnknownPath(t *testing.T) {
	v := Load(t)
	err := v.ValidateRequest("DELETE", "/teams/1", nil, nil)
	if err == nil {
		t.Fatal("DELETE /teams/1 was accepted")
	}
	var ce *Error
	if !errors.As(err, &ce) {
		t.Fatalf("error is %T, want *Error", err)
	}
	if ce.Part != PartPath {
		t.Errorf("part = %q, want %q", ce.Part, PartPath)
	}
	if !strings.Contains(err.Error(), "/teams/1") {
		t.Errorf("error does not name the route: %v", err)
	}
}

// TestBatchExemption pins PLAN.md Layer 6: the description has no /$batch path
// or schema, so the spec-based validators say so explicitly instead of
// reporting a path mismatch, and the local envelope validator takes over.
func TestBatchExemption(t *testing.T) {
	v := Load(t)
	body := []byte(`{"requests":[{"id":"1","method":"GET","url":"/me"}]}`)

	for _, err := range []error{
		v.ValidateRequest("POST", BatchPath, nil, body),
		v.ValidateResponse("POST", BatchPath, 200, body),
	} {
		if !errors.Is(err, ErrBatchExempt) {
			t.Fatalf("batch call returned %v, want ErrBatchExempt", err)
		}
		if !strings.Contains(err.Error(), "$batch") {
			t.Errorf("exemption error does not mention $batch: %v", err)
		}
	}

	// A query string must not defeat the exemption.
	if err := v.ValidateRequest("POST", BatchPath+"?$foo=1", nil, body); !errors.Is(err, ErrBatchExempt) {
		t.Errorf("batch with a query string returned %v, want ErrBatchExempt", err)
	}

	if err := ValidateBatchEnvelope(body); err != nil {
		t.Errorf("a valid batch envelope was rejected: %v", err)
	}
}

func TestValidateBatchEnvelope(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{"valid", `{"requests":[{"id":"1","method":"GET","url":"/me"}]}`, ""},
		{"valid with headers and body", `{"requests":[{"id":"a","method":"POST","url":"/me/messages","headers":{"Content-Type":"application/json"},"body":{"x":1}}]}`, ""},
		{"not json", `{`, "not valid JSON"},
		{"missing requests", `{}`, "requests"},
		{"empty requests", `{"requests":[]}`, "at least 1"},
		{"too many requests", `{"requests":[` + strings.Repeat(`{"id":"x","method":"GET","url":"/me"},`, 20) + `{"id":"y","method":"GET","url":"/me"}]}`, "at most 20"},
		{"missing id", `{"requests":[{"method":"GET","url":"/me"}]}`, "id"},
		{"duplicate id", `{"requests":[{"id":"1","method":"GET","url":"/me"},{"id":"1","method":"GET","url":"/me"}]}`, "duplicate"},
		{"bad method", `{"requests":[{"id":"1","method":"HEAD","url":"/me"}]}`, "HEAD"},
		{"absolute url", `{"requests":[{"id":"1","method":"GET","url":"https://graph.microsoft.com/v1.0/me"}]}`, "must be a relative Graph path"},
		{"unknown field", `{"requests":[{"id":"1","method":"GET","url":"/me"}],"extra":1}`, "unknown field"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateBatchEnvelope([]byte(tc.body))
			if tc.want == "" {
				if err != nil {
					t.Fatalf("expected success, got %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected an error mentioning %q", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q does not mention %q", err.Error(), tc.want)
			}
			var ce *Error
			if !errors.As(err, &ce) {
				t.Fatalf("error is %T, want *Error", err)
			}
			if ce.Path != BatchPath || ce.Part != PartBody {
				t.Errorf("error parts = (%s, %s), want (%s, %s)", ce.Path, ce.Part, BatchPath, PartBody)
			}
		})
	}
}

// TestValidateResponse covers response validation for the message GET, both a
// correct chatMessage and a response that violates the schema.
func TestValidateResponse(t *testing.T) {
	v := Load(t)
	path := "/teams/5e3ce6c0-2b1f-4285-8d4b-75ee78787346/channels/19:abc@thread.tacv2/messages/1615971548136"

	ok, err := json.Marshal(map[string]any{
		"id":                   "1615971548136",
		"messageType":          "message",
		"createdDateTime":      "2024-01-01T00:00:00Z",
		"lastModifiedDateTime": "2024-01-01T00:00:00Z",
		"body":                 map[string]any{"content": "hi", "contentType": "text"},
		"from":                 map[string]any{"user": map[string]any{"id": "u1", "displayName": "A"}},
		"mentions":             []any{},
		"reactions":            []any{},
		"attachments":          []any{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := v.ValidateResponse("GET", path, 200, ok); err != nil {
		t.Fatalf("a plausible chatMessage response failed: %v", err)
	}

	bad := []byte(`{"createdDateTime":"not-a-date"}`)
	err = v.ValidateResponse("GET", path, 200, bad)
	if err == nil {
		t.Fatal("a response with a malformed createdDateTime was accepted")
	}
	var ce *Error
	if !errors.As(err, &ce) {
		t.Fatalf("error is %T, want *Error", err)
	}
	if ce.Part != PartResponse {
		t.Errorf("part = %q, want %q", ce.Part, PartResponse)
	}
	if !strings.Contains(ce.Value, "200") {
		t.Errorf("error does not echo the status: %q", ce.Value)
	}

	// 200 matches the spec's 2XX pattern, so it is documented.
	if err := v.ValidateResponse("GET", path, 200, []byte(`{}`)); err != nil {
		t.Errorf("200 should match the operation's 2XX response: %v", err)
	}
	// 300 is not covered by any response the operation declares (the spec lists
	// 2XX, 4XX and 5XX), so it must be reported rather than silently accepted.
	if err := v.ValidateResponse("GET", path, 300, []byte(`{}`)); err == nil {
		t.Error("an undocumented status code was accepted")
	}
}

// TestErrorFormat pins the shape of a contract failure, since a failing test
// has to be actionable without a debugger.
func TestErrorFormat(t *testing.T) {
	e := &Error{Method: "POST", Path: "/me/messages", Part: PartBody, Value: `{"x":1}`, Err: errors.New("boom")}
	got := e.Error()
	for _, want := range []string{"POST", "/me/messages", "body", "boom", `{"x":1}`} {
		if !strings.Contains(got, want) {
			t.Errorf("formatted error %q does not mention %q", got, want)
		}
	}
	if errors.Is(e, errors.New("other")) {
		t.Error("Is matched an unrelated error")
	}
	if !strings.Contains(e.Error(), "contract violation") {
		t.Error("error does not identify itself as a contract violation")
	}
}

// batchOf builds a batch envelope with n sub-requests, each with a unique id.
func batchOf(n int) string {
	var b strings.Builder
	b.WriteString(`{"requests":[`)
	for i := range n {
		if i > 0 {
			b.WriteString(",")
		}
		fmt.Fprintf(&b, `{"id":"%d","method":"GET","url":"/me"}`, i)
	}
	b.WriteString(`]}`)
	return b.String()
}

// TestBatchEnvelopeSchemaMatchesChecks keeps the written-down batch contract
// (batchEnvelopeSchema, which is OURS, not Microsoft's) in step with the
// hand-written validation, and documents the two places where the description
// gives us nothing to lean on: there is no /$batch path and no batch schema.
func TestBatchEnvelopeSchemaMatchesChecks(t *testing.T) {
	var schema struct {
		Properties struct {
			Requests struct {
				MinItems int `json:"minItems"`
				MaxItems int `json:"maxItems"`
				Items    struct {
					Required   []string `json:"required"`
					Properties map[string]struct {
						Enum []string `json:"enum"`
					} `json:"properties"`
				} `json:"items"`
			} `json:"requests"`
		} `json:"properties"`
	}
	if err := json.Unmarshal([]byte(batchEnvelopeSchema), &schema); err != nil {
		t.Fatalf("batchEnvelopeSchema is not valid JSON: %v", err)
	}
	req := schema.Properties.Requests
	if req.MinItems != 1 {
		t.Errorf("schema minItems = %d, want 1", req.MinItems)
	}
	if req.MaxItems != 20 {
		t.Errorf("schema maxItems = %d, want 20 (refs/graph/concepts/json-batching.md: at most 20 requests)", req.MaxItems)
	}
	for _, want := range []string{"id", "method", "url"} {
		if !slices.Contains(req.Items.Required, want) {
			t.Errorf("schema does not require %q", want)
		}
	}
	gotMethods := req.Items.Properties["method"].Enum
	wantMethods := []string{"GET", "POST", "PUT", "PATCH", "DELETE"}
	if len(gotMethods) != len(wantMethods) {
		t.Fatalf("schema method enum = %v, want %v", gotMethods, wantMethods)
	}
	for i := range wantMethods {
		if gotMethods[i] != wantMethods[i] {
			t.Errorf("schema method enum = %v, want %v", gotMethods, wantMethods)
		}
	}

	// And the checks must agree with the schema's boundaries.
	if err := ValidateBatchEnvelope([]byte(batchOf(20))); err != nil {
		t.Errorf("a batch at the documented 20-request limit was rejected: %v", err)
	}
	if err := ValidateBatchEnvelope([]byte(batchOf(21))); err == nil {
		t.Error("a batch over the documented 20-request limit was accepted")
	}
}

// TestValidateRequestPathParameterError covers a bad value in a path parameter,
// which is a different branch of the failure classifier from a query parameter
// or a body.
func TestValidateRequestPathParameterError(t *testing.T) {
	v := Load(t)
	// A path parameter whose name does not appear in the trimmed template.
	err := v.ValidateRequest("GET", "/teams/abc/not-a-channel/messages", nil, nil)
	if err == nil {
		t.Fatal("an unknown route was accepted")
	}
	var ce *Error
	if !errors.As(err, &ce) {
		t.Fatalf("error is %T, want *Error", err)
	}
	if ce.Part != PartPath {
		t.Errorf("part = %q, want %q", ce.Part, PartPath)
	}
	if !strings.Contains(ce.Value, "not-a-channel") {
		t.Errorf("error does not echo the offending path: %q", ce.Value)
	}
}

// TestValidateRequestWrongMethod covers the method-mismatch branch: the path
// exists, the method does not.
func TestValidateRequestWrongMethod(t *testing.T) {
	v := Load(t)
	err := v.ValidateRequest("DELETE", "/me/people", nil, nil)
	if err == nil {
		t.Fatal("DELETE /me/people was accepted")
	}
	if !strings.Contains(err.Error(), "HTTP method") {
		t.Errorf("error %q does not explain the method mismatch", err)
	}
}

func TestNewValidatorRejectsBadInput(t *testing.T) {
	if _, err := newValidator([]byte("not: [valid"), []byte("GET /x refs/graph/api-reference/v1.0/api/x.md\n")); err == nil {
		t.Error("a malformed spec was accepted")
	}
	if _, err := newValidator(trimmedSpecYAML, []byte("GET /x\n")); err == nil {
		t.Error("a malformed route list was accepted")
	}
	if _, err := newValidator(trimmedSpecYAML, []byte("# only comments\n")); err == nil {
		t.Error("an empty route list was accepted")
	}
}

// TestValidateBatchEnvelopeTruncation covers the truncation used to keep a large
// offending body out of the test log: a body long enough to be cut, and short
// enough to be echoed whole.
func TestValidateBatchEnvelopeTruncation(t *testing.T) {
	long := `{"requests":[` + strings.Repeat(`{"id":"1","method":"GET","url":"/me"},`, 600) + `]}`
	err := ValidateBatchEnvelope([]byte(long))
	if err == nil {
		t.Fatal("an oversized envelope was accepted")
	}
	if !strings.Contains(err.Error(), "\u2026") {
		t.Errorf("a long offending value was not truncated: %v", err)
	}

	short := `{"requests":[{"id":"1","method":"GET","url":"/me"}],"extra":true}`
	err = ValidateBatchEnvelope([]byte(short))
	if err == nil {
		t.Fatal("an envelope with an unknown top-level field was accepted")
	}
	if !strings.Contains(err.Error(), "unknown field") {
		t.Errorf("error %q does not explain the unknown field", err)
	}
	if strings.Contains(err.Error(), "\u2026") {
		t.Errorf("a short offending value was truncated: %v", err)
	}
}
