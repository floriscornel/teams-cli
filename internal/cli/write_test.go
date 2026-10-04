package cli

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/floriscornel/teams-cli/internal/graph"
	"github.com/floriscornel/teams-cli/internal/output"
	"github.com/floriscornel/teams-cli/internal/ref"
)

// Tests for the Phase 4 write plumbing: the flag matrix, the body building, the
// mention spellings, the `teams api` body syntax, the request paths and the
// scope gate each command opens with. The end-to-end behaviour lives in the
// testscript scripts (testdata/script/*_write.txtar).

// flagCmd returns a command with the shared write flags bound, so a test can
// exercise contentTypeMode/validate the way a command does.
func flagCmd(flags *messageFlags) *cobra.Command {
	cmd := &cobra.Command{Use: "test"}
	flags.bind(cmd)
	return cmd
}

func TestContentTypeModeAndValidation(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		flags   func(*messageFlags)
		want    string
		wantErr string
	}{
		{name: "markdown by default", want: "md"},
		{name: "text", args: []string{"--text"}, want: "text"},
		{name: "html", args: []string{"--html"}, want: "html"},
		{name: "explicit markdown", args: []string{"--md"}, want: "md"},
		{name: "text and html", args: []string{"--text", "--html"}, wantErr: "mutually exclusive"},
		{name: "markdown and text", args: []string{"--md", "--text"}, wantErr: "mutually exclusive"},
		{name: "mention needs html", args: []string{"--text"}, flags: func(f *messageFlags) { f.mentions = []string{"alice"} }, wantErr: "needs an HTML body"},
		{name: "importance", args: []string{"--importance", "urgent"}, want: "md"},
		{name: "bad importance", args: []string{"--importance", "urgentish"}, wantErr: "use normal, high or urgent"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var flags messageFlags
			cmd := flagCmd(&flags)
			cmd.SetArgs(tc.args)
			if err := cmd.ParseFlags(tc.args); err != nil {
				t.Fatalf("parse flags: %v", err)
			}
			if tc.flags != nil {
				tc.flags(&flags)
			}
			got, err := flags.validate(cmd)
			switch {
			case tc.wantErr != "":
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("validate = (%q, %v), want an error mentioning %q", got, err, tc.wantErr)
				}
				if code := output.CodeOf(err); code != output.CodeUsage {
					t.Errorf("exit code = %d, want %d", code, output.CodeUsage)
				}
			case err != nil:
				t.Fatalf("validate: %v", err)
			case got != tc.want:
				t.Errorf("mode = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestBuildItemBody(t *testing.T) {
	tests := []struct {
		mode        string
		text        string
		wantContent string
		wantType    string
	}{
		{"md", "**bold**", "<p><strong>bold</strong></p>", "html"},
		{"md", "  ", "", "html"},
		{"html", "<p onclick=\"x()\">hi</p>", "<p>hi</p>", "html"},
		{"text", "  plain & simple  ", "plain & simple", "text"},
	}
	for _, tc := range tests {
		t.Run(tc.mode+"/"+tc.text, func(t *testing.T) {
			body, err := buildItemBody(tc.mode, tc.text)
			if err != nil {
				t.Fatalf("buildItemBody: %v", err)
			}
			if body.Content != tc.wantContent || body.ContentType != tc.wantType {
				t.Errorf("body = %+v, want %q/%q", body, tc.wantContent, tc.wantType)
			}
		})
	}
}

func TestMentionHandles(t *testing.T) {
	person := ref.Ref{User: "alice@example.com", UserMail: "alice@example.com", UserName: "Alice Example", Target: "alice"}
	got := mentionHandles("alice@example.com", person)
	// The typed form first, then the alias, the display name and the local part
	// of the address - de-duplicated, because the alias and the address usually
	// share it.
	want := []string{"alice@example.com", "alice", "Alice Example"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("mentionHandles = %v, want %v", got, want)
	}
	// A typed @name loses its @ and its quotes, so the handle matches the body.
	got = mentionHandles(`@"Alice Example"`, person)
	if got[0] != "Alice Example" {
		t.Errorf("mentionHandles = %v, want the quoted spelling without its quotes", got)
	}
}

func TestEscapeAttribute(t *testing.T) {
	got := escapeAttribute(`a"b&c<d>`)
	want := "a&quot;b&amp;c&lt;d&gt;"
	if got != want {
		t.Errorf("escapeAttribute = %q, want %q", got, want)
	}
}

func TestGeneratedFileMarkup(t *testing.T) {
	if got := inlineImageHTML("1", "chart.png"); got != `<p><img src="../hostedContents/1/$value" alt="chart.png"></p>` {
		t.Errorf("inlineImageHTML = %q", got)
	}
	if got := attachmentHTML("att-1"); got != `<p><attachment id="att-1"></attachment></p>` {
		t.Errorf("attachmentHTML = %q", got)
	}
	if got := appendToBody("<p>hi</p>", []string{attachmentHTML("x"), inlineImageHTML("1", "a.png")}); got !=
		`<p>hi</p><p><attachment id="x"></attachment></p><p><img src="../hostedContents/1/$value" alt="a.png"></p>` {
		t.Errorf("appendToBody = %q", got)
	}
	if got := appendToBody("  ", nil); got != "  " {
		t.Errorf("appendToBody with nothing to add = %q, want the body unchanged", got)
	}
}

func TestMessageTargetPathRendering(t *testing.T) {
	tests := []struct {
		name   string
		target graph.MessageTarget
		want   string
	}{
		{"channel collection", graph.MessageTarget{TeamID: "t", ChannelID: "c"}, "/teams/t/channels/c/messages"},
		{"channel message", graph.MessageTarget{TeamID: "t", ChannelID: "c", MessageID: "m"}, "/teams/t/channels/c/messages/m"},
		{"channel reply", graph.MessageTarget{TeamID: "t", ChannelID: "c", MessageID: "root", ReplyID: "r"}, "/teams/t/channels/c/messages/root/replies/r"},
		{"chat collection", graph.MessageTarget{ChatID: "19:c@thread.v2"}, "/chats/19:c@thread.v2/messages"},
		{"chat message", graph.MessageTarget{ChatID: "19:c@thread.v2", MessageID: "m"}, "/chats/19:c@thread.v2/messages/m"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := messageTargetPath(tc.target); got != tc.want {
				t.Errorf("messageTargetPath = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestMessageIDPrefersTheReply(t *testing.T) {
	if got := messageID(graph.MessageTarget{MessageID: "root", ReplyID: "reply"}); got != "reply" {
		t.Errorf("messageID = %q, want the message the user named", got)
	}
	if got := messageID(graph.MessageTarget{MessageID: "root"}); got != "root" {
		t.Errorf("messageID = %q", got)
	}
}

func TestAPIBodySyntax(t *testing.T) {
	body, err := apiBody([]string{"body.contentType=html", "body.content=<p>hi</p>"}, []string{"importance=high", "count=3", "flag=true", "blank=null", "name=bob"}, "", strings.NewReader(""))
	if err != nil {
		t.Fatalf("apiBody: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	inner, ok := got["body"].(map[string]any)
	if !ok || inner["contentType"] != "html" || inner["content"] != "<p>hi</p>" {
		t.Errorf("body = %+v, want the nested object -f builds", got["body"])
	}
	// -F keeps JSON types, and a value that is not a literal stays a string.
	if _, ok := got["count"].(float64); !ok {
		t.Errorf("count = %T, want a number", got["count"])
	}
	if got["flag"] != true {
		t.Errorf("flag = %#v, want true", got["flag"])
	}
	if value, present := got["blank"]; !present || value != nil {
		t.Errorf("blank = %#v (present %v), want null", value, present)
	}
	if got["name"] != "bob" {
		t.Errorf("name = %#v, want the string", got["name"])
	}
	if _, nested := got["importance"]; !nested {
		t.Errorf("importance is missing: %+v", got)
	}
}

func TestAPIBodyErrors(t *testing.T) {
	tests := []struct {
		name    string
		fields  []string
		typed   []string
		input   string
		wantErr string
	}{
		{name: "input with fields", fields: []string{"a=b"}, input: "x.json", wantErr: "do not combine"},
		{name: "field without a value", fields: []string{"nonsense"}, wantErr: "use key=value"},
		{name: "typed field without a value", typed: []string{"nonsense"}, wantErr: "use key=value"},
		{name: "a scalar cannot hold a nested field", fields: []string{"a=1", "a.b=2"}, wantErr: "cannot hold"},
		{name: "empty key", fields: []string{"=1"}, wantErr: "use key=value"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := apiBody(tc.fields, tc.typed, tc.input, strings.NewReader(""))
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("apiBody = %v, want an error mentioning %q", err, tc.wantErr)
			}
		})
	}
}

func TestAPIBodyRejectsInvalidJSONInput(t *testing.T) {
	if _, err := validateJSONBody([]byte("{not json")); err == nil {
		t.Fatal("want a usage error for a body that is not JSON")
	}
	if body, err := validateJSONBody([]byte("  ")); err != nil || body != nil {
		t.Errorf("validateJSONBody(blank) = (%v, %v), want no body and no error", body, err)
	}
	if body, err := validateJSONBody([]byte(`{"a":1}`)); err != nil || string(body) != `{"a":1}` {
		t.Errorf("validateJSONBody = (%q, %v)", body, err)
	}
}

func TestTypedValue(t *testing.T) {
	tests := map[string]any{
		"true": true, "false": false, "null": nil, "3": int64(3), "3.5": 3.5, "03": int64(3), "bob": "bob", "": "",
	}
	for in, want := range tests {
		if got := typedValue(in); got != want {
			t.Errorf("typedValue(%q) = %#v, want %#v", in, got, want)
		}
	}
}

// writeScopeHarness returns a harness whose token carries exactly scopes, so a
// command's scope gate can be exercised without a session or a network call.
func writeScopeHarness(t *testing.T, scopes ...string) *harness {
	t.Helper()
	h := newHarness(t)
	h.app.Hooks.GrantedScopes = scopes
	return h
}

func TestWriteCommandsCheckTheirScopeFirst(t *testing.T) {
	readOnly := []string{"User.Read", "Chat.Read", "Chat.ReadBasic", "ChannelMessage.Read.All"}
	tests := []struct {
		name    string
		args    []string
		scopes  []string
		wantErr string
	}{
		{
			name: "post to a channel needs ChannelMessage.Send", args: []string{"post", "Engineering/General", "hi"},
			scopes: readOnly, wantErr: "ChannelMessage.Send",
		},
		{
			name: "post to a chat needs ChatMessage.Send or Chat.ReadWrite", args: []string{"post", "19:bob@thread.v2", "hi"},
			scopes: readOnly, wantErr: "ChatMessage.Send",
		},
		{
			name: "post with a file needs the upload scope", args: []string{"post", "Engineering/General", "hi", "--file", "notes.txt"},
			// The message scope is granted, so the failure is the file scope.
			scopes: append(append([]string(nil), readOnly...), "ChannelMessage.Send"), wantErr: "Files.ReadWrite.All",
		},
		{
			name: "edit needs the read-write scope", args: []string{"edit", "Engineering/General/m-1", "hi"},
			scopes: readOnly, wantErr: "ChannelMessage.ReadWrite",
		},
		{
			name: "delete needs the read-write scope", args: []string{"delete", "Engineering/General/m-1"},
			scopes: readOnly, wantErr: "ChannelMessage.ReadWrite",
		},
		{
			name: "react needs the send scope", args: []string{"react", "Engineering/General/m-1", "x"},
			scopes: readOnly, wantErr: "ChannelMessage.Send",
		},
		{
			name: "chat create needs Chat.Create or Chat.ReadWrite", args: []string{"chat", "create", "--with", "bob@example.com"},
			scopes: []string{"User.Read"}, wantErr: "Chat.Create",
		},
		{
			name: "add-member needs Chat.ReadWrite", args: []string{"chat", "add-member", "19:bob@thread.v2", "bob@example.com"},
			scopes: []string{"Chat.Read"}, wantErr: "Chat.ReadWrite",
		},
		{
			name: "mark-read needs Chat.ReadWrite", args: []string{"chat", "mark-read", "19:bob@thread.v2"},
			scopes: []string{"Chat.Read"}, wantErr: "Chat.ReadWrite",
		},
		{
			name: "chat delete needs the incremental scope", args: []string{"chat", "delete", "19:bob@thread.v2", "--yes"},
			scopes: []string{"Chat.ReadWrite"}, wantErr: "Chat.ManageDeletion.All",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := writeScopeHarness(t, tc.scopes...)
			err := h.run(tc.args...)
			if err == nil {
				t.Fatalf("%v: want a scope error, got nil", tc.args)
			}
			if code := output.CodeOf(err); code != output.CodeAuth {
				t.Errorf("exit code = %d, want %d (auth required)", code, output.CodeAuth)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error = %v, want it to name %s", err, tc.wantErr)
			}
			if hint := output.HintOf(err); hint == "" {
				t.Error("a missing scope must carry the fix (which preset or admin consent grants it)")
			}
		})
	}
}

func TestChatDeleteIncrementalScopeHint(t *testing.T) {
	h := writeScopeHarness(t, "Chat.ReadWrite")
	err := h.run("chat", "delete", "19:bob@thread.v2", "--yes")
	if err == nil {
		t.Fatal("want a scope error")
	}
	// Non-interactive, so there is no re-consent prompt: the hint explains that
	// the scope is requested on demand.
	if hint := output.HintOf(err); !strings.Contains(hint, "teams chat delete") {
		t.Errorf("hint = %q, want the command that requests the scope", hint)
	}
}

func TestClassifyChat(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		want    bool
		wantErr bool
	}{
		{name: "name path", raw: "Engineering/General", want: false},
		{name: "name path with a message", raw: "Engineering/General/m-1", want: false},
		{name: "channel id", raw: "19:general@thread.tacv2", want: false},
		{name: "channel link", raw: "https://teams.microsoft.com/l/channel/19:general@thread.tacv2/General?groupId=team-eng", want: false},
		{name: "channel message link", raw: "https://teams.microsoft.com/l/message/19:general@thread.tacv2/m-1002?groupId=team-eng", want: false},
		{name: "chat id", raw: "19:bob@thread.v2", want: true},
		{name: "chat link", raw: "https://teams.microsoft.com/l/chat/19:bob@thread.v2/conversations", want: true},
		{name: "chat message link", raw: "https://teams.microsoft.com/l/message/19:bob@thread.v2/c-1?context=%7B%22contextType%22%3A%22chat%22%7D", want: true},
		{name: "person", raw: "@bob", want: true},
		{name: "email", raw: "bob@example.com", want: true},
		{name: "bare name", raw: "Release train", want: true},
		{name: "empty", raw: "", wantErr: true},
		{name: "an app link the parser refuses", raw: "https://teams.microsoft.com/l/app/abc", wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := classifyChat(tc.raw)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("classifyChat(%q) = %v, want an error", tc.raw, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("classifyChat(%q): %v", tc.raw, err)
			}
			if got != tc.want {
				t.Errorf("classifyChat(%q) = %v, want %v", tc.raw, got, tc.want)
			}
		})
	}
}

func TestChatCreateRejectsAnEmptyInviteeList(t *testing.T) {
	h := writeScopeHarness(t, "Chat.ReadWrite")
	err := h.run("chat", "create")
	if err == nil || !strings.Contains(err.Error(), "at least one participant") {
		t.Fatalf("error = %v, want the --with requirement", err)
	}
}
