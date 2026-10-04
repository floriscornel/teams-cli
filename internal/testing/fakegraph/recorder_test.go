package fakegraph

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/floriscornel/teams-cli/internal/graph"
)

// This file covers the request recorder: tests must be able to assert the exact
// payloads the CLI sends, including mention `<at id="N">` tags, the
// `hostedContents[]` temporary id pairing and attachment references (PLAN.md
// Layer 2, "Request recorder").

// mentionPost is a message POST shaped the way teams post must shape it.
func mentionPost() map[string]any {
	return map[string]any{
		"subject": "Deploy notes",
		"body": map[string]string{
			"contentType": "html",
			"content":     `<p>hi <at id="0">Alice Example</at></p><img src="../hostedContents/1/$value">`,
		},
		"mentions": []any{map[string]any{
			"id":          0,
			"mentionText": "Alice Example",
			"mentioned": map[string]any{"user": map[string]any{
				"id":               "u-alice",
				"displayName":      "Alice Example",
				"userIdentityType": "aadUser",
			}},
		}},
		"hostedContents": []any{map[string]any{
			"@microsoft.graph.temporaryId": "1",
			"contentBytes":                 base64.StdEncoding.EncodeToString([]byte("fake-png-bytes")),
			"contentType":                  "image/png",
		}},
		"attachments": []any{map[string]any{
			"id":          "att-1",
			"contentType": "reference",
			"contentUrl":  "https://contoso.example/spec.pdf",
			"name":        "spec.pdf",
		}},
	}
}

func TestRecorderCapturesTheExactPayload(t *testing.T) {
	srv := newTestServer(t)
	c := newClient(t, srv)

	resp, err := c.Do(context.Background(), graph.Request{
		Method: http.MethodPost,
		Path:   "/teams/t-eng/channels/c-general/messages",
		Body:   mentionPost(),
		Header: http.Header{"Prefer": []string{PreferUnknownEnumMembers}},
	})
	if err != nil {
		t.Fatalf("POST message: %v", err)
	}
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d, want 201", resp.StatusCode)
	}
	var created messageWire
	if err := resp.Decode(&created); err != nil {
		t.Fatal(err)
	}

	recs := srv.RequestsFor(http.MethodPost, "/teams/t-eng/channels/c-general/messages")
	if len(recs) != 1 {
		t.Fatalf("recorded %d requests, want 1", len(recs))
	}
	rec := recs[0]
	if rec.Seq < 1 {
		t.Fatalf("Seq = %d, want a positive call index", rec.Seq)
	}
	if rec.Status != http.StatusCreated {
		t.Fatalf("recorded status = %d, want 201", rec.Status)
	}
	if got := rec.Header.Get("Prefer"); got != PreferUnknownEnumMembers {
		t.Fatalf("recorded Prefer = %q", got)
	}
	var decoded map[string]any
	if err := rec.DecodeBody(&decoded); err != nil {
		t.Fatalf("DecodeBody: %v", err)
	}
	// The client escapes HTML in the JSON body, so the assertions read the
	// decoded payload rather than the raw bytes.
	body := decoded["body"].(map[string]any)["content"].(string)
	if !strings.Contains(body, `<at id="0">`) {
		t.Fatalf("the recorded body lost its mention tag: %q", body)
	}
	if !strings.Contains(body, `../hostedContents/1/$value`) {
		t.Fatalf("the recorded body lost the hosted content reference: %q", body)
	}
	hostedEntry := decoded["hostedContents"].([]any)[0].(map[string]any)
	if hostedEntry["@microsoft.graph.temporaryId"] != "1" {
		t.Fatalf("recorded hostedContents temporaryId = %v", hostedEntry["@microsoft.graph.temporaryId"])
	}
	attachments := decoded["attachments"].([]any)[0].(map[string]any)
	if attachments["contentUrl"] != "https://contoso.example/spec.pdf" || attachments["contentType"] != "reference" {
		t.Fatalf("recorded attachment = %#v", attachments)
	}
	mentions, ok := decoded["mentions"].([]any)
	if !ok || len(mentions) != 1 {
		t.Fatalf("mentions = %#v", decoded["mentions"])
	}
	mentioned := mentions[0].(map[string]any)["mentioned"].(map[string]any)["user"].(map[string]any)
	if mentioned["userIdentityType"] != "aadUser" || mentioned["displayName"] != "Alice Example" {
		t.Fatalf("mention user = %#v, want displayName and userIdentityType: aadUser", mentioned)
	}

	// The fake stored what it was told, so a read returns the same payload.
	read, err := c.Do(context.Background(), graph.Request{Method: http.MethodGet, Path: "/teams/t-eng/channels/c-general/messages/" + created.ID})
	if err != nil {
		t.Fatalf("GET created message: %v", err)
	}
	var fetched messageWire
	if err := read.Decode(&fetched); err != nil {
		t.Fatal(err)
	}
	if fetched.Subject != "Deploy notes" || len(fetched.Mentions) != 1 || len(fetched.Attachments) != 1 {
		t.Fatalf("stored message = %+v", fetched)
	}
	if fetched.Mentions[0].Mentioned == nil || fetched.Mentions[0].Mentioned.User == nil ||
		fetched.Mentions[0].Mentioned.User.UserIdentityType != "aadUser" {
		t.Fatalf("stored mention = %+v", fetched.Mentions[0])
	}
	if fetched.Attachments[0].ContentURL != "https://contoso.example/spec.pdf" {
		t.Fatalf("stored attachment = %+v", fetched.Attachments[0])
	}

	// The hosted content is readable through its documented byte route.
	hosted, err := c.Do(context.Background(), graph.Request{
		Method: http.MethodGet,
		Path:   "/teams/t-eng/channels/c-general/messages/" + created.ID + "/hostedContents",
	})
	if err != nil {
		t.Fatalf("list hosted contents: %v", err)
	}
	var list struct {
		Value []hostedContentWire `json:"value"`
	}
	if err := hosted.Decode(&list); err != nil {
		t.Fatal(err)
	}
	if len(list.Value) != 1 || list.Value[0].ID != "1" || list.Value[0].ContentType != "image/png" {
		t.Fatalf("hosted contents = %+v", list.Value)
	}
	value, err := c.Do(context.Background(), graph.Request{
		Method: http.MethodGet,
		Path:   "/teams/t-eng/channels/c-general/messages/" + created.ID + "/hostedContents/1/$value",
	})
	if err != nil {
		t.Fatalf("fetch hosted content: %v", err)
	}
	if string(value.Body) != "fake-png-bytes" {
		t.Fatalf("hosted content bytes = %q", value.Body)
	}
}

func TestRecorderRejectsUnpairedHostedContent(t *testing.T) {
	srv := newTestServer(t)
	c := newClient(t, srv)

	payload := mentionPost()
	payload["body"] = map[string]string{"contentType": "html", "content": "<p>no image reference</p>"}
	_, err := c.Do(context.Background(), graph.Request{
		Method: http.MethodPost,
		Path:   "/teams/t-eng/channels/c-general/messages",
		Body:   payload,
	})
	// The image reference is gone, so the temporaryId no longer pairs with the
	// body; Graph's requirement is that they match
	// (refs/graph/api-reference/v1.0/resources/chatmessagehostedcontent.md).
	if got := errStatus(err); got != 400 {
		t.Fatalf("status = %d (err %v), want 400", got, err)
	}
}

func TestRecorderRejectsMentionWithoutTag(t *testing.T) {
	srv := newTestServer(t)
	c := newClient(t, srv)

	payload := mentionPost()
	payload["body"] = map[string]string{"contentType": "html", "content": "<p>the sanitizer ate my mention</p>"}
	_, err := c.Do(context.Background(), graph.Request{
		Method: http.MethodPost,
		Path:   "/teams/t-eng/channels/c-general/messages",
		Body:   payload,
	})
	if got := errStatus(err); got != 400 {
		t.Fatalf("status = %d (err %v), want 400: mentions[] must have their <at> tag", got, err)
	}
}

func TestRecorderResetAndFiltering(t *testing.T) {
	srv := newTestServer(t)
	c := newClient(t, srv)

	if _, err := c.Do(context.Background(), graph.Request{Method: http.MethodGet, Path: "/me"}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Do(context.Background(), graph.Request{Method: http.MethodGet, Path: "/me/chats"}); err != nil {
		t.Fatal(err)
	}
	if got := len(srv.Requests()); got != 2 {
		t.Fatalf("recorded %d requests, want 2", got)
	}
	if got := len(srv.RequestsFor(http.MethodGet, "/me")); got != 1 {
		t.Fatalf("RequestsFor /me = %d, want 1", got)
	}
	if last := srv.LastRequest(); last == nil || last.Path != "/me/chats" {
		t.Fatalf("LastRequest = %+v", last)
	}
	if got := srv.RequestsFor(http.MethodPost, "/me"); len(got) != 0 {
		t.Fatalf("RequestsFor POST /me = %d, want 0", len(got))
	}

	srv.ResetRequests()
	if got := len(srv.Requests()); got != 0 {
		t.Fatalf("after ResetRequests: %d requests", got)
	}
	if srv.LastRequest() != nil {
		t.Fatal("LastRequest did not return nil after ResetRequests")
	}
}

func TestRecorderCapturesQueryAndHeaders(t *testing.T) {
	srv := newTestServer(t)
	c := newClient(t, srv)
	if _, err := graph.GetPage[chatWire](context.Background(), c, "/me/chats",
		url.Values{"$top": {"2"}}, graph.WithHeader("Prefer", PreferUnknownEnumMembers)); err != nil {
		t.Fatalf("GET chats: %v", err)
	}
	rec := srv.LastRequest()
	if rec.Query.Get("$top") != "2" {
		t.Fatalf("recorded query = %v", rec.Query)
	}
	if rec.RawPath != DefaultBasePath+"/me/chats" {
		t.Fatalf("RawPath = %q", rec.RawPath)
	}
	if rec.Header.Get("Authorization") == "" || rec.Header.Get("client-request-id") == "" {
		t.Fatalf("recorded headers = %v", rec.Header)
	}
}

func TestRecordedBodyDecodesNilSafely(t *testing.T) {
	rec := &RecordedRequest{}
	var v map[string]any
	if err := rec.DecodeBody(&v); err != nil {
		t.Fatalf("DecodeBody on an empty body: %v", err)
	}
	if rec.BodyString() != "" {
		t.Fatalf("BodyString = %q", rec.BodyString())
	}
	var raw json.RawMessage
	rec2 := &RecordedRequest{Body: []byte(`{"a":1}`)}
	if err := rec2.DecodeBody(&raw); err != nil {
		t.Fatal(err)
	}
}
