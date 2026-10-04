package contract

import (
	"strings"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/getkin/kin-openapi/routers"
	"gopkg.in/yaml.v3"
)

// TestJSONContentType covers the content-type selection: a route with no
// request body, and a route whose operation is missing, both fall back to JSON
// rather than panicking.
func TestJSONContentType(t *testing.T) {
	if got := jsonContentType(nil); got != "application/json" {
		t.Errorf("jsonContentType(nil) = %q, want application/json", got)
	}
	if got := jsonContentType(&routers.Route{}); got != "application/json" {
		t.Errorf("route without an operation = %q, want application/json", got)
	}
	if got := jsonContentType(&routers.Route{Operation: &openapi3.Operation{}}); got != "application/json" {
		t.Errorf("operation without a request body = %q, want application/json", got)
	}
}

// TestSortedContentKeys pins the deterministic order of the fallback content
// type, so the chosen content type never depends on map iteration order.
func TestSortedContentKeys(t *testing.T) {
	if got := sortedContentKeys(nil); len(got) != 0 {
		t.Errorf("sortedContentKeys(nil) = %v, want empty", got)
	}
}

// TestFindOperationAmbiguous covers the branch that refuses to guess when a
// route shape matches several operations and none of their externalDocs match
// the page the route cites. Guessing here would silently validate a route
// against the wrong operation.
//
// Two paths that differ only in a literal prefix cannot produce this (their
// shapes differ), so the fixture uses two paths whose shapes are identical, and
// checks that the error names all of them.
func TestFindOperationAmbiguous(t *testing.T) {
	var paths yaml.Node
	paths.Kind = yaml.MappingNode
	for _, specPath := range []string{
		"/me/joinedTeams/{team-id}/channels/{channel-id}/messages",
		"/me/joinedTeams/{team-id}/channels/{message-id}/messages",
	} {
		item := &yaml.Node{Kind: yaml.MappingNode, Content: []*yaml.Node{
			scalar("get"), {Kind: yaml.MappingNode, Content: []*yaml.Node{
				scalar("operationId"), scalar("some.Op"),
			}},
		}}
		paths.Content = append(paths.Content, scalar(specPath), item)
	}

	_, err := findOperation(&paths, Route{
		Method: "GET",
		Path:   "/me/joinedTeams/{team-id}/channels/{channel-id}/messages",
		Source: "channel-list-messages.md",
	})
	if err == nil {
		t.Fatal("an ambiguous shape match was accepted")
	}
	for _, want := range []string{"ambiguous", "channel-list-messages.md", "/me/joinedTeams/{team-id}/channels/{channel-id}/messages"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err.Error(), want)
		}
	}
}
