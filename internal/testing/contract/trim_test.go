package contract

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// TestPathShape covers the normalization that lets the two sources be compared
// at all: parameter names, the parenthesised spelling a few pages use,
// alternation groups, and the version prefix absolute examples carry.
func TestPathShape(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"/teams/{team-id}/channels/{channel-id}/messages", "/teams/{}/channels/{}/messages"},
		{"/teams/(team-id)/channels/{channel-id}/messages/{message-id}", "/teams/{}/channels/{}/messages/{}"},
		{"/users/{user-id | user-principal-name}/chats/{chat-id}", "/users/{}/chats/{}"},
		{"/drives/{drive-id}/items/{driveItem-id}/content", "/drives/{}/items/{}/content"},
		{"/search/query", "/search/query"},
		{"/me/chats?$top=50", "/me/chats"},
		{
			"/chats/{chat-id}/messages/{chatMessage-id}/hostedContents/{hosted-content-id}/$value",
			"/chats/{}/messages/{}/hostedContents/{}/$value",
		},
	}
	for _, tc := range tests {
		if got := pathShape(tc.in); got != tc.want {
			t.Errorf("pathShape(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestNormalizeDocPath covers the request-line normalization, including the two
// shapes that broke naive parsing: a "HTTP/1.1" suffix and a path with a space
// inside an alternation group.
func TestNormalizeDocPath(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"GET", ""},
		{"/me", "/me"},
		{"/me HTTP/1.1", "/me"},
		{"https://graph.microsoft.com/v1.0/me/chats", "/me/chats"},
		{"https://graph.microsoft.com/v1.0/chats/x/messages/y/hostedContents/$value", "/chats/x/messages/y/hostedContents/$value"},
		{"/users/{user-id | user-principal-name}/chats/{chat-id}/messages", "/users/{}/chats/{}/messages"},
		{"", ""},
		{"relative/path", ""},
	}
	for _, tc := range tests {
		if got := normalizeDocPath(tc.in); got != tc.want {
			t.Errorf("normalizeDocPath(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestParseAPIReferencePage(t *testing.T) {
	body := strings.Join([]string{
		"# title",
		"```http",
		"GET /teams/{team-id}/channels",
		"```",
		"",
		"``` http",
		"POST /teams/{team-id}/channels/{channel-id}/messages",
		"```",
		"",
		"```msgraph-interactive",
		"GET https://graph.microsoft.com/v1.0/me/joinedTeams",
		"HTTP/1.1 200 OK",
		"```",
		"",
		"```json",
		"GET /not/a/request/shape",
		"```",
	}, "\n")

	got := parseAPIReferencePage(body)
	want := []docRoute{
		{"GET", "/teams/{}/channels"},
		{"POST", "/teams/{}/channels/{}/messages"},
		{"GET", "/me/joinedTeams"},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d routes %v, want %d", len(got), got, len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("route %d = %v, want %v", i, got[i], want[i])
		}
	}
}

func TestPageHasRoute(t *testing.T) {
	page := []docRoute{
		{"GET", "/teams/{}/channels/{}/messages/{}/hostedContents"},
		{"GET", "/chats/{}/messages/{}/hostedContents/{}/$value"},
		{"POST", "/users/{}/chats/{}/messages/{}/softDelete"},
	}
	tests := []struct {
		name   string
		method string
		shape  string
		want   bool
	}{
		{"exact", "GET", "/teams/{}/channels/{}/messages/{}/hostedContents", true},
		{"wrong method", "POST", "/teams/{}/channels/{}/messages/{}/hostedContents", false},
		{"wrong tail", "GET", "/teams/{}/channels/{}/messages/{}/filesFolder", false},
		{"documented under a container", "POST", "/chats/{}/messages/{}/softDelete", true},
		{"unrelated", "GET", "/me/people", false},
		{"different tail", "GET", "/teams/{}/channels/{}/messages/{}/filesFolder", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := pageHasRoute(page, tc.method, tc.shape); got != tc.want {
				t.Errorf("pageHasRoute(%q, %q) = %v, want %v", tc.method, tc.shape, got, tc.want)
			}
		})
	}
}

func TestStrictSuffixMatch(t *testing.T) {
	tests := []struct {
		got, want string
		wantOK    bool
	}{
		// The chat form is documented under the /users/{id}/chats container.
		{"/users/{}/chats/{}/messages/{}/softDelete", "/chats/{}/messages/{}/softDelete", true},
		{"/users/{}/chats/{}/messages/{}/setReaction", "/chats/{}/messages/{}/setReaction", true},
		// A different tail must not match.
		{"/users/{}/chats/{}/messages/{}/replies/{}/softDelete", "/chats/{}/messages/{}/softDelete", false},
		{"/me/people", "/teams/{}/channels/{}/messages/{}/hostedContents", false},
		{"/teams/{}/channels/{}/messages/{}/softDelete", "/chats/{}/messages/{}/softDelete", false},
		// Equal literal counts are the equal-length comparison's job.
		{"/chats/{}/messages/{}/softDelete", "/chats/{}/messages/{}/softDelete", false},
	}
	for _, tc := range tests {
		got := strings.Split(tc.got, "/")
		want := strings.Split(tc.want, "/")
		if res := strictSuffixMatch(got, want); res != tc.wantOK {
			t.Errorf("strictSuffixMatch(%q, %q) = %v, want %v", tc.got, tc.want, res, tc.wantOK)
		}
	}
}

func split(p string) []string { return strings.Split(p, "/") }

func TestAliasParameters(t *testing.T) {
	// Same segment count, different parameter names: rename.
	got := aliasParameters(
		"/drives/{drive-id}/items/{driveItem-id}/content",
		"/drives/{drive-id}/items/{item-id}/content")
	if len(got) != 1 || got["driveItem-id"] != "item-id" {
		t.Errorf("aliasParameters = %v, want {driveItem-id: item-id}", got)
	}

	// The description folds the team into a bare parameter, so there is no
	// name to rename; the path item's parameters are dropped by the caller.
	if got := aliasParameters(
		"/groups/{group-id}/team/channels/{channel-id}/messages",
		"/teams/{team-id}/channels/{channel-id}/messages"); len(got) != 0 {
		t.Errorf("aliasParameters = %v, want none", got)
	}
}

// TestRenamePathParameters covers the rename the alias rule needs so that a
// request against /teams/{team-id}/... matches a path item whose parameters came
// from /groups/{group-id}/team/....
func TestRenamePathParameters(t *testing.T) {
	params := &yaml.Node{Kind: yaml.SequenceNode, Content: []*yaml.Node{
		{Kind: yaml.MappingNode, Content: []*yaml.Node{
			scalar("name"), scalar("group-id"),
			scalar("in"), scalar("path"),
		}},
		{Kind: yaml.MappingNode, Content: []*yaml.Node{
			scalar("name"), scalar("channel-id"),
			scalar("in"), scalar("path"),
		}},
	}}
	renamePathParameters(params, map[string]string{"group-id": "team-id"})
	if got := mappingValue(params.Content[0], "name").Value; got != "team-id" {
		t.Errorf("first parameter renamed to %q, want team-id", got)
	}
	if got := mappingValue(params.Content[1], "name").Value; got != "channel-id" {
		t.Errorf("second parameter became %q, want it untouched", got)
	}
}

// TestFindOperationRejectsUnknownRoute pins the failure the trimmer must produce
// when a route has no counterpart in the description at all.
func TestFindOperationRejectsUnknownRoute(t *testing.T) {
	root := mirrorHeavy(t)
	raw, err := os.ReadFile(filepath.Join(root, openAPISource))
	if err != nil {
		t.Fatal(err)
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	top, err := documentMapping(&doc)
	if err != nil {
		t.Fatal(err)
	}
	_, err = findOperation(top["paths"], Route{
		Method: "POST",
		Path:   "/teams/{team-id}/channels/{channel-id}/messages/{chatMessage-id}/unsupportedAction",
		Source: "chatmessage-post.md",
	})
	if err == nil {
		t.Fatal("findOperation accepted a route the description does not declare")
	}
	if !strings.Contains(err.Error(), "no matching path") {
		t.Errorf("error %q does not explain the missing path", err)
	}

	// A route the description declares but the api-reference does not document
	// on the claimed page fails the page check the generator runs before any of
	// this, which is what keeps CreateHostedContents out: the operation exists
	// (operationId groups.team.channels.messages.CreateHostedContents) but no
	// api-reference page describes a standalone hostedContents POST.
	root = hasRefs(t)
	if err := verifyRoutesAgainstAPIReference(root); err != nil {
		t.Fatalf("the committed route list must pass its own page check: %v", err)
	}
}

// TestStripODataTypeRequired is the repair rule's unit test: it strips the
// entry, drops an emptied required list, and leaves everything else alone.
func TestStripODataTypeRequired(t *testing.T) {
	schema := &yaml.Node{Kind: yaml.MappingNode, Content: []*yaml.Node{
		scalar("required"),
		{Kind: yaml.SequenceNode, Content: []*yaml.Node{scalar(odataTypeKey)}},
		scalar("properties"),
		{Kind: yaml.MappingNode, Content: []*yaml.Node{
			scalar(odataTypeKey), {Kind: yaml.MappingNode, Content: []*yaml.Node{scalar("type"), scalar("string")}},
		}},
	}}
	nested := &yaml.Node{Kind: yaml.MappingNode, Content: []*yaml.Node{
		scalar("allOf"), {Kind: yaml.SequenceNode, Content: []*yaml.Node{
			{Kind: yaml.MappingNode, Content: []*yaml.Node{
				scalar("required"), {Kind: yaml.SequenceNode, Content: []*yaml.Node{
					scalar("body"), scalar(odataTypeKey), scalar("subject"),
				}},
			}},
		}},
	}}

	changed := stripFromRequiredArrays(schema) + stripFromRequiredArrays(nested)
	if changed != 2 {
		t.Errorf("stripped %d entries, want 2", changed)
	}
	if mappingValue(schema, "required") != nil {
		t.Error("the emptied required list was not dropped")
	}
	// The property declaration must survive.
	props := mappingValue(schema, "properties")
	if mappingValue(props, odataTypeKey) == nil {
		t.Error("the @odata.type property declaration was removed; only the requirement may be")
	}
	// Other entries and their order must survive.
	got := requiredFromAllOf(nested)
	want := []string{"body", "subject"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("remaining required = %v, want %v", got, want)
	}
}

// TestPruneDiscriminatorMappings is the other repair rule's unit test: entries
// whose target was pruned go, entries whose target was kept stay, and an
// emptied mapping is dropped entirely.
func TestPruneDiscriminatorMappings(t *testing.T) {
	keep := map[string]map[string]bool{"schemas": {"kept": true}}
	disc := &yaml.Node{Kind: yaml.MappingNode, Content: []*yaml.Node{
		scalar("propertyName"), scalar("@odata.type"),
		scalar("mapping"),
		{Kind: yaml.MappingNode, Content: []*yaml.Node{
			scalar("#microsoft.graph.kept"), scalar("#/components/schemas/kept"),
			scalar("#microsoft.graph.gone"), scalar("#/components/schemas/gone"),
		}},
	}}
	removed := pruneDiscriminatorMapping(disc, keep)
	if removed != 1 {
		t.Errorf("pruned %d entries, want 1", removed)
	}
	mapping := mappingValue(disc, "mapping")
	if mapping == nil || len(mapping.Content) != 2 {
		t.Fatalf("mapping = %v, want one surviving pair", mapping)
	}
	if mapping.Content[0].Value != "#microsoft.graph.kept" {
		t.Errorf("surviving entry = %q, want #microsoft.graph.kept", mapping.Content[0].Value)
	}

	// An entry that is not a component ref is left for the post-condition check.
	odd := &yaml.Node{Kind: yaml.MappingNode, Content: []*yaml.Node{
		scalar("mapping"), {Kind: yaml.MappingNode, Content: []*yaml.Node{
			scalar("#x"), scalar("not-a-ref"),
		}},
	}}
	if n := pruneDiscriminatorMapping(odd, keep); n != 0 {
		t.Errorf("pruned %d non-ref entries, want 0", n)
	}

	// Every entry pruned: the mapping key goes too.
	all := &yaml.Node{Kind: yaml.MappingNode, Content: []*yaml.Node{
		scalar("mapping"), {Kind: yaml.MappingNode, Content: []*yaml.Node{
			scalar("#gone"), scalar("#/components/schemas/gone"),
		}},
	}}
	if n := pruneDiscriminatorMapping(all, keep); n != 1 {
		t.Errorf("pruned %d entries, want 1", n)
	}
	if mappingValue(all, "mapping") != nil {
		t.Error("the emptied mapping was not dropped")
	}
}

func TestSplitComponentRef(t *testing.T) {
	tests := []struct {
		ref       string
		cat, name string
		wantOK    bool
	}{
		{"#/components/schemas/a", "schemas", "a", true},
		{"#/components/responses/error", "responses", "error", true},
		{"#/components/schemas/a/b", "schemas", "a/b", true},
		{"#/paths/~1me", "", "", false},
		{"#/components/schemas", "", "", false},
		{"#/components//a", "", "", false},
	}
	for _, tc := range tests {
		cat, name, ok := splitComponentRef(tc.ref)
		if ok != tc.wantOK || cat != tc.cat || name != tc.name {
			t.Errorf("splitComponentRef(%q) = (%q, %q, %v), want (%q, %q, %v)",
				tc.ref, cat, name, ok, tc.cat, tc.name, tc.wantOK)
		}
	}
}

func TestExternalDocsPage(t *testing.T) {
	tests := []struct {
		url  string
		want string
	}{
		{"https://learn.microsoft.com/graph/api/channel-post-messages?view=graph-rest-1.0", "channel-post-messages"},
		{"https://learn.microsoft.com/graph/api/chatmessage-get", "chatmessage-get"},
		{"https://learn.microsoft.com/graph/somewhere-else", ""},
		{"", ""},
	}
	for _, tc := range tests {
		op := &yaml.Node{Kind: yaml.MappingNode, Content: []*yaml.Node{
			scalar("externalDocs"), {Kind: yaml.MappingNode, Content: []*yaml.Node{
				scalar("url"), scalar(tc.url),
			}},
		}}
		if got := externalDocsPage(op); got != tc.want {
			t.Errorf("externalDocsPage(%q) = %q, want %q", tc.url, got, tc.want)
		}
	}
	// An operation without externalDocs reports nothing.
	if got := externalDocsPage(&yaml.Node{Kind: yaml.MappingNode}); got != "" {
		t.Errorf("externalDocsPage(no docs) = %q, want empty", got)
	}
}

// TestRouteOrderingSorted pins the deterministic ordering of routes.txt, which
// is what makes the refs-check diff meaningful.
func TestRouteOrderingSorted(t *testing.T) {
	lines := Routes()
	// routes.txt is sorted by path, then by method, so "POST /chats" comes
	// before "DELETE /chats/{chat-id}".
	for i := 1; i < len(lines); i++ {
		prev, cur := splitRoute(lines[i-1]), splitRoute(lines[i])
		if prev[1] > cur[1] || (prev[1] == cur[1] && prev[0] > cur[0]) {
			t.Errorf("routes are not sorted by path then method: %q before %q", lines[i-1], lines[i])
		}
	}
	// The rendered file must agree with the sorted list.
	rendered, err := parseRoutesText(routesText())
	if err != nil {
		t.Fatal(err)
	}
	if len(rendered) != len(lines) {
		t.Fatalf("routesText has %d routes, Routes() has %d", len(rendered), len(lines))
	}
	for i, r := range rendered {
		if r.Method+" "+r.Path != lines[i] {
			t.Errorf("entry %d: rendered %q, Routes() has %q", i, r.Method+" "+r.Path, lines[i])
		}
	}
}

func splitRoute(line string) [2]string {
	method, path, _ := strings.Cut(line, " ")
	return [2]string{method, path}
}

func TestParseRoutesTextRejectsJunk(t *testing.T) {
	if _, err := parseRoutesText("GET /a\n"); err == nil {
		t.Error("a two-field line was accepted")
	}
	if _, err := parseRoutesText("# comment\n\nGET /a refs/graph/api-reference/v1.0/api/x.md\n"); err != nil {
		t.Errorf("comments and blank lines must be skipped: %v", err)
	}
}
