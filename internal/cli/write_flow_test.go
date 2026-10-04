package cli

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/floriscornel/teams-cli/internal/graph"
	"github.com/floriscornel/teams-cli/internal/output"
	"github.com/floriscornel/teams-cli/internal/testing/fakegraph"
)

// In-process write tests: the same commands as the testscript scripts, run
// against the fake Graph with the Layer 6 contract hook installed, so a test can
// assert on the recorded traffic as well as on stdout.

const (
	flowMe      = "user-1"
	flowBob     = "user-2"
	flowTeamID  = "team-eng"
	flowChannel = "19:general@thread.tacv2"
)

func newWriteHarness(t *testing.T, model fakegraph.Model) (*harness, *fakegraph.Server) {
	t.Helper()
	srv := fakegraph.New(t, fakegraph.Options{Model: model, Contract: fakegraph.WithContract(t)})
	h := newHarness(t)
	h.graphURL = srv.URL()
	h.applyHooks()
	h.useAccessToken(t, strings.Join([]string{
		"User.Read", "User.ReadBasic.All", "Team.ReadBasic.All", "TeamMember.Read.All",
		"Channel.ReadBasic.All", "ChannelMessage.Read.All", "ChannelMessage.Send", "ChannelMessage.ReadWrite",
		"Chat.Read", "Chat.ReadBasic", "Chat.ReadWrite", "ChatMessage.Send", "Chat.Create",
		"Files.ReadWrite.All", "Files.ReadWrite", "People.Read",
	}, " "))
	return h, srv
}

func writeFlowModel() fakegraph.Model {
	return fakegraph.Model{
		Me: flowMe,
		Users: []fakegraph.User{
			{ID: flowMe, DisplayName: "Alice Example", UserPrincipalName: "alice@example.com", Mail: "alice@example.com"},
			{ID: flowBob, DisplayName: "Bob Builder", UserPrincipalName: "bob@example.com", Mail: "bob@example.com", Relevance: 0.9},
		},
		Teams: []fakegraph.Team{{
			ID: flowTeamID, DisplayName: "Engineering",
			Channels: []fakegraph.Channel{{
				ID: flowChannel, DisplayName: "General",
				Messages: []fakegraph.Message{
					{ID: "m-1", AuthorID: flowBob, Created: time.Date(2026, 10, 4, 9, 0, 0, 0, time.UTC), Body: "<p>hello</p>"},
				},
			}},
		}},
	}
}

func TestPostToAChannelWithAMention(t *testing.T) {
	h, srv := newWriteHarness(t, writeFlowModel())

	out := h.mustRun("post", "Engineering/General", "hello @bob, please look", "--mention", "bob@example.com")
	if !strings.Contains(out, "posted") {
		t.Errorf("stdout = %q, want the posted id", out)
	}
	calls := srv.RequestsFor(http.MethodPost, "/teams/"+flowTeamID+"/channels/"+flowChannel+"/messages")
	if len(calls) != 1 {
		t.Fatalf("POST calls = %d, want 1", len(calls))
	}
	var sent graph.MessagePost
	if err := calls[0].DecodeBody(&sent); err != nil {
		t.Fatalf("decode the recorded body: %v", err)
	}
	if sent.Body.ContentType != "html" || !strings.Contains(sent.Body.Content, `<at id="0">Bob Builder</at>`) {
		t.Errorf("body = %+v, want the converted markdown with the mention tag", sent.Body)
	}
	if len(sent.Mentions) != 1 {
		t.Fatalf("mentions = %+v, want one", sent.Mentions)
	}
	user := sent.Mentions[0].Mentioned.User
	if user == nil || user.ID != flowBob || user.DisplayName != "Bob Builder" || user.UserIdentityType != "aadUser" {
		t.Errorf("mention user = %+v, want the documented shape", user)
	}
}

func TestPostDryRunPlansTheUpload(t *testing.T) {
	// The upload a real run would perform, with the numbers an attachment needs.
	// This lives here rather than in the testscript script: the script's fixture
	// bytes depend on how the checkout translates line endings, and an exact byte
	// count would only hold on the machines that checked it out with LF.
	h, srv := newWriteHarness(t, writeFlowModel())
	dir := t.TempDir()
	path := filepath.Join(dir, "notes.txt")
	content := "# deploy notes\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	out := h.mustRun("post", "Engineering/General", "later", "--file", path, "--dry-run")
	var doc struct {
		Uploads []struct {
			File        string `json:"file"`
			Name        string `json:"name"`
			Size        int64  `json:"size"`
			ContentType string `json:"contentType"`
			Kind        string `json:"kind"`
		} `json:"uploads"`
	}
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("the dry run is not JSON: %v (%q)", err, out)
	}
	if len(doc.Uploads) != 1 {
		t.Fatalf("uploads = %+v, want one", doc.Uploads)
	}
	upload := doc.Uploads[0]
	if upload.Name != "notes.txt" || upload.Kind != "attachment" || upload.ContentType != "text/plain" {
		t.Errorf("upload = %+v", upload)
	}
	if upload.Size != int64(len(content)) {
		t.Errorf("size = %d, want %d", upload.Size, len(content))
	}
	// A dry run really uploads nothing.
	if calls := srv.RequestsFor(http.MethodPut, ""); len(calls) != 0 {
		t.Errorf("the dry run sent %d PUTs, want none", len(calls))
	}
}

func TestPostDryRunSendsNothing(t *testing.T) {
	h, srv := newWriteHarness(t, writeFlowModel())

	out := h.mustRun("post", "Engineering/General", "never sent", "--dry-run")
	if !strings.Contains(out, `"dryRun": true`) || !strings.Contains(out, "<p>never sent</p>") {
		t.Errorf("stdout = %q, want the printed request", out)
	}
	if calls := srv.RequestsFor(http.MethodPost, "/teams/"+flowTeamID+"/channels/"+flowChannel+"/messages"); len(calls) != 0 {
		t.Errorf("POST calls = %d, want none for a dry run", len(calls))
	}
}

func TestPostToAPersonCreatesTheChatOnce(t *testing.T) {
	// The fake has no 1:1 chat with Bob, so the write-mode person form must
	// create it: PLAN.md:166 picks the create for writes, and POST /chats
	// returns the existing chat when there is one.
	h, srv := newWriteHarness(t, writeFlowModel())

	h.mustRun("post", "@bob", "lunch?")
	creates := srv.RequestsFor(http.MethodPost, "/chats")
	if len(creates) != 1 {
		t.Fatalf("POST /chats calls = %d, want 1", len(creates))
	}
	var chat struct {
		ChatType string `json:"chatType"`
		Members  []struct {
			Roles []string `json:"roles"`
		} `json:"members"`
	}
	if err := creates[0].DecodeBody(&chat); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if chat.ChatType != "oneOnOne" || len(chat.Members) != 2 {
		t.Errorf("create body = %+v, want a one-on-one chat with two members", chat)
	}
	for _, member := range chat.Members {
		if len(member.Roles) != 1 || member.Roles[0] != "owner" {
			t.Errorf("member = %+v, want the owner role", member)
		}
	}
	// The message went into the chat the create returned.
	var posted bool
	for _, rec := range srv.Requests() {
		if rec.Method == http.MethodPost && strings.HasPrefix(rec.Path, "/chats/") && strings.HasSuffix(rec.Path, "/messages") {
			posted = true
		}
	}
	if !posted {
		t.Errorf("no chat message was posted; traffic: %s", readTrafficOf(srv))
	}

	// The person -> chat mapping is cached, so a second post reuses it instead
	// of creating again (PLAN.md:166).
	h.mustRun("post", "@bob", "still lunch?")
	if got := len(srv.RequestsFor(http.MethodPost, "/chats")); got != 1 {
		t.Errorf("POST /chats calls = %d, want the cached chat to be reused", got)
	}
}

func TestEditDeleteAndReactThroughTheFake(t *testing.T) {
	h, srv := newWriteHarness(t, writeFlowModel())

	h.mustRun("edit", "Engineering/General/m-1", "edited text", "--text")
	patches := srv.RequestsFor(http.MethodPatch, "/teams/"+flowTeamID+"/channels/"+flowChannel+"/messages/m-1")
	if len(patches) != 1 {
		t.Fatalf("PATCH calls = %d, want 1", len(patches))
	}
	var patch graph.MessagePatch
	if err := patches[0].DecodeBody(&patch); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if patch.Body == nil || patch.Body.Content != "edited text" || patch.Body.ContentType != "text" {
		t.Errorf("patch = %+v", patch)
	}

	h.mustRun("react", "Engineering/General/m-1", "👍")
	if calls := srv.RequestsFor(http.MethodPost, "/teams/"+flowTeamID+"/channels/"+flowChannel+"/messages/m-1/setReaction"); len(calls) != 1 {
		t.Errorf("setReaction calls = %d, want 1", len(calls))
	}
	h.mustRun("react", "Engineering/General/m-1", "👍", "--remove")
	if calls := srv.RequestsFor(http.MethodPost, "/teams/"+flowTeamID+"/channels/"+flowChannel+"/messages/m-1/unsetReaction"); len(calls) != 1 {
		t.Errorf("unsetReaction calls = %d, want 1", len(calls))
	}

	h.mustRun("delete", "Engineering/General/m-1")
	if calls := srv.RequestsFor(http.MethodPost, "/teams/"+flowTeamID+"/channels/"+flowChannel+"/messages/m-1/softDelete"); len(calls) != 1 {
		t.Errorf("softDelete calls = %d, want 1", len(calls))
	}
}

func TestReplyInAChannelThreadThroughTheFake(t *testing.T) {
	h, srv := newWriteHarness(t, writeFlowModel())

	h.mustRun("reply", "Engineering/General/m-1", "on it")
	if calls := srv.RequestsFor(http.MethodPost, "/teams/"+flowTeamID+"/channels/"+flowChannel+"/messages/m-1/replies"); len(calls) != 1 {
		t.Errorf("reply calls = %d, want 1", len(calls))
	}
}

func TestReadBodyTextSources(t *testing.T) {
	h := newHarness(t)

	// The argument wins.
	got, err := h.app.readBodyText([]string{"from the argument"})
	if err != nil || got != "from the argument" {
		t.Errorf("readBodyText = (%q, %v)", got, err)
	}

	// "-" reads stdin.
	h.app.Stdin = strings.NewReader("from stdin\n")
	if got, err = h.app.readBodyText([]string{"-"}); err != nil || got != "from stdin\n" {
		t.Errorf("readBodyText(-) = (%q, %v)", got, err)
	}

	// No text and no terminal is a usage error that says how to pass one.
	h.app.Stdin = strings.NewReader("")
	_, err = h.app.readBodyText(nil)
	if err == nil || !strings.Contains(err.Error(), "no message text") {
		t.Fatalf("readBodyText() = %v, want the usage error", err)
	}
	if hint := output.HintOf(err); !strings.Contains(hint, "pass the text") {
		t.Errorf("hint = %q, want the way to pass text", hint)
	}
	if code := output.CodeOf(err); code != output.CodeUsage {
		t.Errorf("exit code = %d, want %d", code, output.CodeUsage)
	}
}

func TestEditorTextRunsTheConfiguredEditor(t *testing.T) {
	if runtime.GOOS == "windows" {
		// The stand-in editor is a POSIX shell script, and exec refuses a .sh on
		// Windows; the code under test (run $EDITOR on a temp file, read it back)
		// is the same everywhere.
		t.Skip("the stand-in editor is a shell script")
	}
	dir := t.TempDir()
	editor := filepath.Join(dir, "editor.sh")
	script := "#!/bin/sh\nprintf 'from the editor\\n' > \"$1\"\n"
	// 0700 because the file is executed: it stands in for the user's $EDITOR.
	if err := os.WriteFile(editor, []byte(script), 0o700); err != nil { //nolint:gosec // a test's own executable helper
		t.Fatal(err)
	}
	t.Setenv("EDITOR", editor)
	t.Setenv("VISUAL", "")

	h := newHarness(t)
	// A terminal is what makes the editor the default source (PLAN.md:176), and
	// the harness's printer is not interactive, so make one that is.
	h.app.Printer = output.New(output.Options{Out: &h.stdout, Err: &h.stderr, Interactive: true})
	got, err := h.app.readBodyText(nil)
	if err != nil {
		t.Fatalf("readBodyText: %v", err)
	}
	if got != "from the editor\n" {
		t.Errorf("text = %q, want the editor's content", got)
	}
}

func TestEditorTextWithoutAnEditor(t *testing.T) {
	t.Setenv("EDITOR", "")
	t.Setenv("VISUAL", "")
	h := newHarness(t)
	h.app.Printer = output.New(output.Options{Out: &h.stdout, Err: &h.stderr, Interactive: true})
	_, err := h.app.readBodyText(nil)
	if err == nil || !strings.Contains(err.Error(), "no editor configured") {
		t.Fatalf("readBodyText = %v, want the no-editor usage error", err)
	}
}

// readTrafficOf renders the recorded traffic, for a failure message.
func readTrafficOf(srv *fakegraph.Server) string {
	parts := make([]string, 0, len(srv.Requests()))
	for _, rec := range srv.Requests() {
		parts = append(parts, rec.Method+" "+rec.Path)
	}
	return strings.Join(parts, ", ")
}
