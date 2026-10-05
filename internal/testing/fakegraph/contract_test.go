package fakegraph

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/floriscornel/teams-cli/internal/graph"
	"github.com/floriscornel/teams-cli/internal/testing/contract"
)

// This file is the fakegraph side of the Layer 6 contract tests (PLAN.md
// "Contract tests against Microsoft's OpenAPI spec"): every route in the
// committed list that the fake implements is called through the real
// internal/graph client, and both the request and the response are validated
// against the trimmed Graph v1.0 description with kin-openapi.
//
// Two shapes of coverage:
//
//   - [WithContract] installs the validation as a live hook on the server, for
//     tests that want every call checked;
//   - TestContractRoutesThroughTheRealClient walks contract.Routes() explicitly
//     so a route whose path the trimmed spec does not shape can be logged as a
//     skip instead of failing the build.
//
// /$batch is exempt from the spec (the description has no batch path or schema
// at all, refs/INDEX.md), so it is validated with contract.ValidateBatchEnvelope
// instead.

// contractCall is the concrete request a route template is exercised with.
type contractCall struct {
	path  string
	query url.Values
	body  any
}

// contractCalls maps every route in contract.Routes() the fake implements to a
// concrete request against testModel. A template that is missing here is a test
// bug, not a skip.
// calendarViewSelectForTest is the $select the calendar commands send, spelled
// out here because the fakegraph package cannot import the graph package's
// constant without a cycle. The value is plans/calendar.md §4.3; isOnlineMeeting
// must stay in the list or calendarView omits onlineMeeting
// (plans/calendar.md §3, F3).
const calendarViewSelectForTest = "id,subject,start,end,isAllDay,isCancelled,showAs,organizer," +
	"isOrganizer,isOnlineMeeting,onlineMeeting,onlineMeetingProvider,location,responseStatus," +
	"webLink,type,seriesMasterId"

var contractCalls = map[string]contractCall{
	// Teams and channels.
	"GET /me":                                    {path: "/me"},
	"GET /me/joinedTeams":                        {path: "/me/joinedTeams"},
	"GET /users":                                 {path: "/users"},
	"GET /users/{user-id}":                       {path: "/users/u-alice"},
	"GET /me/people":                             {path: "/me/people"},
	"GET /teams/{team-id}":                       {path: "/teams/t-eng"},
	"GET /teams/{team-id}/members":               {path: "/teams/t-eng/members"},
	"GET /teams/{team-id}/channels":              {path: "/teams/t-eng/channels"},
	"GET /teams/{team-id}/channels/{channel-id}": {path: "/teams/t-eng/channels/c-general"},

	// Channel messages and replies.
	"GET /teams/{team-id}/channels/{channel-id}/messages":                                                    {path: "/teams/t-eng/channels/c-general/messages"},
	"POST /teams/{team-id}/channels/{channel-id}/messages":                                                   {path: "/teams/t-eng/channels/c-general/messages", body: messageBody("posted through the contract test")},
	"GET /teams/{team-id}/channels/{channel-id}/messages/{chatMessage-id}":                                   {path: "/teams/t-eng/channels/c-general/messages/cm-1"},
	"PATCH /teams/{team-id}/channels/{channel-id}/messages/{chatMessage-id}":                                 {path: "/teams/t-eng/channels/c-general/messages/cm-1", body: messageBody("edited through the contract test")},
	"GET /teams/{team-id}/channels/{channel-id}/messages/{chatMessage-id}/replies":                           {path: "/teams/t-eng/channels/c-general/messages/cm-2/replies"},
	"POST /teams/{team-id}/channels/{channel-id}/messages/{chatMessage-id}/replies":                          {path: "/teams/t-eng/channels/c-general/messages/cm-2/replies", body: messageBody("reply through the contract test")},
	"GET /teams/{team-id}/channels/{channel-id}/messages/{chatMessage-id}/replies/{reply-id}":                {path: "/teams/t-eng/channels/c-general/messages/cm-2/replies/cr-1"},
	"PATCH /teams/{team-id}/channels/{channel-id}/messages/{chatMessage-id}/replies/{reply-id}":              {path: "/teams/t-eng/channels/c-general/messages/cm-2/replies/cr-1", body: messageBody("edited reply")},
	"POST /teams/{team-id}/channels/{channel-id}/messages/{chatMessage-id}/softDelete":                       {path: "/teams/t-eng/channels/c-general/messages/cm-1/softDelete"},
	"POST /teams/{team-id}/channels/{channel-id}/messages/{chatMessage-id}/replies/{reply-id}/softDelete":    {path: "/teams/t-eng/channels/c-general/messages/cm-2/replies/cr-1/softDelete"},
	"POST /teams/{team-id}/channels/{channel-id}/messages/{chatMessage-id}/setReaction":                      {path: "/teams/t-eng/channels/c-general/messages/cm-2/setReaction", body: map[string]string{"reactionType": "👍"}},
	"POST /teams/{team-id}/channels/{channel-id}/messages/{chatMessage-id}/unsetReaction":                    {path: "/teams/t-eng/channels/c-general/messages/cm-2/unsetReaction", body: map[string]string{"reactionType": "👍"}},
	"POST /teams/{team-id}/channels/{channel-id}/messages/{chatMessage-id}/replies/{reply-id}/setReaction":   {path: "/teams/t-eng/channels/c-general/messages/cm-2/replies/cr-2/setReaction", body: map[string]string{"reactionType": "❤"}},
	"POST /teams/{team-id}/channels/{channel-id}/messages/{chatMessage-id}/replies/{reply-id}/unsetReaction": {path: "/teams/t-eng/channels/c-general/messages/cm-2/replies/cr-2/unsetReaction", body: map[string]string{"reactionType": "❤"}},

	// Inline images.
	"GET /teams/{team-id}/channels/{channel-id}/messages/{chatMessage-id}/hostedContents":                    {path: "/teams/t-eng/channels/c-general/messages/cm-1/hostedContents"},
	"GET /teams/{team-id}/channels/{channel-id}/messages/{chatMessage-id}/replies/{reply-id}/hostedContents": {path: "/teams/t-eng/channels/c-general/messages/cm-2/replies/cr-1/hostedContents"},
	"GET /chats/{chat-id}/messages/{chatMessage-id}/hostedContents":                                          {path: "/chats/chat-1on1/messages/gm-hc/hostedContents"},
	"GET /chats/{chat-id}/messages/{chatMessage-id}/hostedContents/{hosted-content-id}/$value":               {path: "/chats/chat-1on1/messages/gm-hc/hostedContents/hc-1/$value"},

	// Chats.
	"GET /me/chats":                                    {path: "/me/chats"},
	"GET /me/chats/{chat-id}":                          {path: "/me/chats/chat-1on1"},
	"POST /chats":                                      {path: "/chats", body: newGroupChatBody()},
	"DELETE /chats/{chat-id}":                          {path: "/chats/chat-group"},
	"POST /chats/{chat-id}/members":                    {path: "/chats/chat-group/members", body: newMemberBody()},
	"GET /chats/{chat-id}/members":                     {path: "/chats/chat-group/members"},
	"POST /chats/{chat-id}/markChatReadForUser":        {path: "/chats/chat-1on1/markChatReadForUser", body: map[string]any{"user": map[string]string{"id": "u-me"}}},
	"POST /chats/{chat-id}/markChatUnreadForUser":      {path: "/chats/chat-1on1/markChatUnreadForUser", body: map[string]any{"user": map[string]string{"id": "u-me"}}},
	"GET /chats/{chat-id}/messages":                    {path: "/chats/chat-1on1/messages"},
	"POST /chats/{chat-id}/messages":                   {path: "/chats/chat-1on1/messages", body: messageBody("chat message through the contract test")},
	"GET /chats/{chat-id}/messages/{chatMessage-id}":   {path: "/chats/chat-1on1/messages/gm-hc"},
	"PATCH /chats/{chat-id}/messages/{chatMessage-id}": {path: "/chats/chat-1on1/messages/gm-hc", body: messageBody("edited chat message")},
	"POST /chats/{chat-id}/messages/replyWithQuote": {path: "/chats/chat-1on1/messages/replyWithQuote", body: map[string]any{
		"messageIds":   []string{"gm-hc"},
		"replyMessage": map[string]any{"body": map[string]string{"contentType": "html", "content": "<p>quoted</p>"}},
	}},
	"POST /chats/{chat-id}/messages/{chatMessage-id}/softDelete": {path: "/chats/chat-1on1/messages/gm-hc/softDelete"},
	// The documented (user-relative) chat soft-delete form is the one the CLI
	// calls, because the /chats form answers 405 live (docs/spike/phase1.md:99).
	"POST /users/{user-id}/chats/{chat-id}/messages/{chatMessage-id}/softDelete": {path: "/users/u-me/chats/chat-1on1/messages/gm-hc/softDelete"},
	"POST /chats/{chat-id}/messages/{chatMessage-id}/setReaction":                {path: "/chats/chat-1on1/messages/gm-hc/setReaction", body: map[string]string{"reactionType": "like"}},
	"POST /chats/{chat-id}/messages/{chatMessage-id}/unsetReaction":              {path: "/chats/chat-1on1/messages/gm-hc/unsetReaction", body: map[string]string{"reactionType": "like"}},

	// Search.
	"POST /search/query": {path: "/search/query", body: map[string]any{
		"requests": []map[string]any{{
			"entityTypes": []string{"chatMessage"},
			"query":       map[string]string{"queryString": "hello"},
			"from":        0,
			"size":        5,
		}},
	}},

	// Calendar (PLAN.md Phase 6).
	"GET /users/{user-id}/calendarView": {path: "/users/u-alice/calendarView", query: url.Values{
		"startDateTime": {"2026-01-01T00:00:00Z"},
		"endDateTime":   {"2026-01-06T00:00:00Z"},
		"$top":          {"100"},
		"$select":       {"id,subject,start,end,isAllDay,isCancelled,showAs,organizer,isOrganizer,isOnlineMeeting,onlineMeeting,onlineMeetingProvider,location,responseStatus,webLink,type,seriesMasterId"},
	}},
	"GET /users/{user-id}/calendar/calendarView": {path: "/users/u-alice/calendar/calendarView", query: url.Values{
		"startDateTime": {"2026-01-02T00:00:00Z"},
		"endDateTime":   {"2026-01-03T00:00:00Z"},
		"$top":          {"100"},
		"$select":       {calendarViewSelectForTest},
	}},
	"GET /me/events/{event-id}":                    {path: "/me/events/ev-standup"},
	"PATCH /me/events/{event-id}":                  {path: "/me/events/ev-standup", body: map[string]any{"subject": "patched"}},
	"DELETE /me/events/{event-id}":                 {path: "/me/events/ev-cancelled"},
	"POST /me/events/{event-id}/accept":            {path: "/me/events/ev-review/accept", body: map[string]any{"comment": "ok", "sendResponse": true}},
	"POST /me/events/{event-id}/tentativelyAccept": {path: "/me/events/ev-review/tentativelyAccept", body: map[string]any{"comment": "maybe", "sendResponse": false}},
	"POST /me/events/{event-id}/decline":           {path: "/me/events/ev-review/decline", body: map[string]any{"comment": "no", "sendResponse": true}},
	"POST /me/events/{event-id}/cancel":            {path: "/me/events/ev-standup/cancel", body: map[string]any{"comment": "cancelled"}},

	// Files.
	"GET /teams/{team-id}/channels/{channel-id}/filesFolder": {path: "/teams/t-eng/channels/c-general/filesFolder"},
	"GET /me/drive": {path: "/me/drive"},
	"GET /drives/{drive-id}/items/{driveItem-id}":          {path: "/drives/drive-t-eng/items/folder-c-general"},
	"GET /drives/{drive-id}/items/{driveItem-id}/children": {path: "/drives/drive-t-eng/items/folder-c-general/children"},
	"GET /drives/{drive-id}/items/{driveItem-id}/content":  {path: "/drives/drive-t-eng/items/file-1/content"},
	"PUT /drives/{drive-id}/items/{driveItem-id}/content":  {path: "/drives/drive-t-eng/items/file-1/content", body: json.RawMessage("uploaded through the contract test")},
	"POST /drives/{drive-id}/items/{driveItem-id}/createUploadSession": {path: "/drives/drive-t-eng/items/folder-c-general/createUploadSession", body: map[string]any{
		"item": map[string]any{"name": "upload.bin"},
	}},
	"POST /drives/{drive-id}/items/{driveItem-id}/createLink": {path: "/drives/drive-t-eng/items/file-1/createLink", body: map[string]string{
		"type": "view", "scope": "organization",
	}},
}

// messageBody is the minimal documented message POST/PATCH body: only `body` is
// required (refs/graph/api-reference/v1.0/api/chatmessage-post.md:47).
func messageBody(content string) map[string]any {
	return map[string]any{"body": map[string]string{"contentType": "html", "content": "<p>" + content + "</p>"}}
}

// newGroupChatBody is a valid POST /chats body: every member carries a role
// (refs/graph/api-reference/v1.0/api/chat-post.md:47).
func newGroupChatBody() map[string]any {
	return map[string]any{
		"chatType": "group",
		"topic":    "contract test group",
		"members": []any{
			map[string]any{"@odata.type": "#microsoft.graph.aadUserConversationMember", "roles": []string{"owner"}, "userId": "u-me"},
			map[string]any{"@odata.type": "#microsoft.graph.aadUserConversationMember", "roles": []string{"owner"}, "userId": "u-alice"},
		},
	}
}

// newMemberBody adds a member with the documented odata bind.
func newMemberBody() map[string]any {
	return map[string]any{
		"@odata.type":     "#microsoft.graph.aadUserConversationMember",
		"roles":           []string{"owner"},
		"user@odata.bind": "https://graph.microsoft.com/v1.0/users('u-carol')",
	}
}

// contractRecorder captures what the graph client put on the wire, so the
// contract test can validate the response bodies too (graph.Client.Do drops the
// response when it is an error).
type contractRecorder struct {
	mu    sync.Mutex
	pairs []contractPair
}

type contractPair struct {
	method   string
	path     string
	query    url.Values
	reqBody  []byte
	status   int
	respBody []byte
}

func (r *contractRecorder) RecordRequest(req *http.Request, body []byte) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.pairs = append(r.pairs, contractPair{
		method:  req.Method,
		path:    strings.TrimPrefix(req.URL.Path, DefaultBasePath),
		query:   req.URL.Query(),
		reqBody: append([]byte(nil), body...),
	})
}

func (r *contractRecorder) RecordResponse(_ *http.Request, status int, body []byte) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.pairs) == 0 {
		return
	}
	last := &r.pairs[len(r.pairs)-1]
	last.status = status
	last.respBody = append([]byte(nil), body...)
}

func (r *contractRecorder) last() (contractPair, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.pairs) == 0 {
		return contractPair{}, false
	}
	return r.pairs[len(r.pairs)-1], true
}

func (r *contractRecorder) reset() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.pairs = nil
}

// TestContractRoutesThroughTheRealClient is the Layer 6 test: it walks the
// committed api-reference route list, calls every route the fake implements
// through the real internal/graph client, and validates both directions against
// the trimmed spec.
func TestContractRoutesThroughTheRealClient(t *testing.T) {
	validator := contract.Load(t)
	srv := New(t, Options{Model: testModel(), Clock: newFakeClock()})
	recorder := &contractRecorder{}
	c := newClient(t, srv, func(o *graph.Options) { o.Recorder = recorder })

	routes := contract.Routes()
	if len(routes) == 0 {
		t.Fatal("contract.Routes() is empty")
	}
	var implemented, requestsValidated, responsesValidated, skipped int
	for _, line := range routes {
		method, template, ok := strings.Cut(line, " ")
		if !ok {
			t.Fatalf("route line %q is not \"METHOD /path\"", line)
		}
		if !srv.Implements(method, template) {
			t.Logf("skip %s: fakegraph does not implement this route yet", line)
			skipped++
			continue
		}
		implemented++
		call, ok := contractCalls[line]
		if !ok {
			t.Errorf("route %s is implemented but has no entry in contractCalls; add one", line)
			continue
		}
		// Each call gets a fresh recorder view, so a retry or a redirect cannot
		// mask the pair under test.
		recorder.reset()
		req := graph.Request{Method: method, Path: call.path, Query: call.query}
		if call.body != nil {
			req.Body = call.body
		}
		_, _ = c.Do(context.Background(), req)
		pair, ok := recorder.last()
		if !ok {
			t.Errorf("%s: the graph client recorded nothing", line)
			continue
		}
		if pair.path != call.path {
			t.Errorf("%s: recorded path %q, want %q", line, pair.path, call.path)
			continue
		}
		if err := validator.ValidateRequest(pair.method, pair.path, pair.query, pair.reqBody); err != nil {
			if isSpecGap(err) {
				t.Logf("skip %s: the trimmed spec does not shape this path: %v", line, err)
				skipped++
				continue
			}
			t.Errorf("%s: request does not validate against the spec: %v", line, err)
			continue
		}
		requestsValidated++
		// Two documented response shapes cannot be checked by a JSON-only
		// validator, and both are already asserted elsewhere in this package:
		//   - 204 No Content (Graph documents it for PATCH and softDelete):
		//     the validator injects Content-Type: application/json, so parsing
		//     an intentionally empty body fails with EOF;
		//   - the binary streams behind .../content and .../$value, where the
		//     spec's content type is application/octet-stream or image/*.
		switch {
		case pair.status == http.StatusNoContent || pair.status == http.StatusResetContent:
			t.Logf("skip %s: %d has no body to validate; the fake answers the documented No Content", line, pair.status)
			skipped++
			continue
		case binaryResponse(call.path):
			t.Logf("skip %s: the response is a binary stream (status %d), which the JSON validator cannot check", line, pair.status)
			skipped++
			continue
		}
		if err := validator.ValidateResponse(pair.method, pair.path, pair.status, pair.respBody); err != nil {
			if isSpecGap(err) {
				t.Logf("skip %s: the trimmed spec does not shape this response: %v", line, err)
				skipped++
				continue
			}
			t.Errorf("%s: response %d does not validate against the spec: %v", line, pair.status, err)
			continue
		}
		responsesValidated++
	}
	// Every implemented route must produce a spec-valid request; responses are
	// validated wherever the spec has a JSON shape (the 204 and binary cases
	// above are checked by the flow and recorder tests instead).
	if requestsValidated != implemented {
		t.Fatalf("validated %d of %d implemented route requests", requestsValidated, implemented)
	}
	if responsesValidated == 0 {
		t.Fatal("no response body was validated against the spec")
	}
	t.Logf("routes: %d committed, %d implemented; requests validated %d, responses validated %d, skipped %d (%d log-only)",
		len(routes), implemented, requestsValidated, responsesValidated, skipped, len(routes)-implemented)
}

// TestContractFakeImplementsMostOfTheCommittedRouteList reports the gap between
// the fake and the committed route list, so a reader of the test output sees
// exactly what is not covered yet.
func TestContractFakeImplementsMostOfTheCommittedRouteList(t *testing.T) {
	srv := New(t, Options{Model: testModel(), Clock: newFakeClock()})
	var missing []string
	for _, line := range contract.Routes() {
		method, template, _ := strings.Cut(line, " ")
		if !srv.Implements(method, template) {
			missing = append(missing, line)
		}
	}
	if len(missing) > 0 {
		t.Logf("fakegraph does not implement %d committed routes: %v", len(missing), missing)
	}
	if !srv.Implements("POST", "/$batch") {
		t.Error("the fake must implement /$batch")
	}
}

// TestContractBatchEnvelopeThroughTheRealClient validates the $batch request the
// real client produces with the local batch schema, because the Graph OpenAPI
// description has no /$batch path or schema (PLAN.md Layer 6).
func TestContractBatchEnvelopeThroughTheRealClient(t *testing.T) {
	srv := New(t, Options{Model: testModel(), Clock: newFakeClock()})
	c := newClient(t, srv)

	items, err := c.Batch(context.Background(), []graph.BatchRequest{
		{ID: "1", Method: http.MethodGet, Path: "/me"},
		{ID: "2", Method: http.MethodGet, Path: "/me/chats", Query: map[string]string{"$top": "5"}},
		{ID: "3", Method: http.MethodPost, Path: "/chats/chat-group/messages", Body: messageBody("batched")},
	})
	if err != nil {
		t.Fatalf("Batch: %v", err)
	}
	if len(items) != 3 {
		t.Fatalf("batch returned %d items, want 3", len(items))
	}
	rec := srv.RequestsFor(http.MethodPost, "/$batch")
	if len(rec) != 1 {
		t.Fatalf("recorded %d $batch requests, want 1", len(rec))
	}
	if err := contract.ValidateBatchEnvelope(rec[0].Body); err != nil {
		t.Fatalf("ValidateBatchEnvelope: %v\n%s", err, rec[0].Body)
	}
	var envelope map[string]any
	if err := json.Unmarshal(rec[0].Body, &envelope); err != nil {
		t.Fatal(err)
	}
	if _, ok := envelope["requests"]; !ok {
		t.Fatalf("batch body has no requests key: %s", rec[0].Body)
	}
	// The response is not exempt: every sub-response is a normal response, so
	// it can be validated per sub-request path.
	validator := contract.Load(t)
	for _, sub := range []struct {
		id, method, path string
	}{
		{"1", http.MethodGet, "/me"},
		{"2", http.MethodGet, "/me/chats"},
	} {
		item, ok := items[sub.id]
		if !ok {
			t.Fatalf("no sub-response %q", sub.id)
		}
		if err := validator.ValidateResponse(sub.method, sub.path, item.Status, item.Body); err != nil {
			t.Errorf("batch sub-response %s (%s %s): %v", sub.id, sub.method, sub.path, err)
		}
	}
}

// TestContractHookOption installs WithContract on the server and proves an
// ordinary call passes, so the option is wired into the request path.
func TestContractHookOption(t *testing.T) {
	srv := New(t, Options{
		Model:    testModel(),
		Clock:    newFakeClock(),
		Contract: WithContract(t),
	})
	c := newClient(t, srv)
	if _, err := graph.GetPage[chatWire](context.Background(), c, "/me/chats", url.Values{"$top": {"5"}}); err != nil {
		t.Fatalf("GET /me/chats under contract validation: %v", err)
	}
	if _, err := c.Do(context.Background(), graph.Request{Method: http.MethodGet, Path: "/me"}); err != nil {
		t.Fatalf("GET /me under contract validation: %v", err)
	}
}

// TestContractHookReportsFailures checks the hook reports a failure through the
// testing handle instead of panicking mid-request.
func TestContractHookReportsFailures(t *testing.T) {
	reporter := &fakeReporter{}
	hook := NewContractHook(stubValidator{err: errors.New("boom")}, reporter)
	srv := New(t, Options{Model: testModel(), Clock: newFakeClock(), Contract: hook})
	c := newClient(t, srv)
	if _, err := c.Do(context.Background(), graph.Request{Method: http.MethodGet, Path: "/me"}); err != nil {
		t.Fatalf("the call itself must still succeed: %v", err)
	}
	if reporter.errors == 0 {
		t.Fatal("the hook did not report the validator failure")
	}
	if !strings.Contains(reporter.last, "GET /me") {
		t.Fatalf("reported message %q, want it to name the call", reporter.last)
	}
}

// TestContractHookSkipsBatchAndPrivatePaths checks the hook ignores /$batch
// (spec-exempt) and the fake's own pre-authenticated URLs.
func TestContractHookSkipsBatchAndPrivatePaths(t *testing.T) {
	reporter := &fakeReporter{}
	hook := NewContractHook(stubValidator{err: contract.ErrBatchExempt}, reporter)
	srv := New(t, Options{Model: testModel(), Clock: newFakeClock(), Contract: hook})
	c := newClient(t, srv)
	if _, err := c.Batch(context.Background(), []graph.BatchRequest{{ID: "1", Method: http.MethodGet, Path: "/me"}}); err != nil {
		t.Fatalf("Batch: %v", err)
	}
	if _, err := c.Do(context.Background(), graph.Request{Method: http.MethodGet, Path: "/drives/drive-t-eng/items/file-1/content"}); err != nil {
		t.Fatalf("download: %v", err)
	}
	if reporter.errors != 0 {
		t.Fatalf("the hook reported %d errors for exempt traffic: %s", reporter.errors, reporter.last)
	}
}

// binaryResponse reports whether a route returns bytes rather than JSON. The
// validator forces Content-Type: application/json, so these streams cannot be
// checked here; the fake's bytes are asserted in flow_test.go and
// recorder_test.go instead.
func binaryResponse(path string) bool {
	return strings.HasSuffix(path, "/$value") || strings.HasSuffix(path, "/content")
}

// isSpecGap reports whether the validator failed because the trimmed spec has no
// matching path or operation, which the brief says to log rather than fail.
func isSpecGap(err error) bool {
	var contractErr *contract.Error
	if !errors.As(err, &contractErr) {
		return false
	}
	return contractErr.Part == contract.PartPath
}

// stubValidator is a ContractValidator that always fails, or always passes.
type stubValidator struct{ err error }

func (v stubValidator) ValidateRequest(string, string, url.Values, []byte) error { return v.err }
func (v stubValidator) ValidateResponse(string, string, int, []byte) error       { return v.err }

// fakeReporter records what a ContractHook reports.
type fakeReporter struct {
	errors int
	last   string
}

func (r *fakeReporter) Helper() {}

func (r *fakeReporter) Errorf(format string, args ...any) {
	r.errors++
	r.last = fmt.Sprintf(format, args...)
}

// Ensure the interface shapes stay as documented.
var (
	_ ContractValidator = stubValidator{}
	_ ContractReporter  = (*fakeReporter)(nil)
	_ graph.Recorder    = (*contractRecorder)(nil)
)
