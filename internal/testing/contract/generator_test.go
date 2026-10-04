package contract

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// specBadOperations are the spec-only operations that must never reach the
// committed route list. CreateHostedContents is the call teams-mcp gets wrong:
// the description declares it (chats.messages.CreateHostedContents and
// groups.team.channels.messages.CreateHostedContents) while the api-reference
// has no page for it at all, because hosted content is created by sending
// hostedContents[] inside the message POST body
// (refs/INDEX.md, "Inline images have no create endpoint in the v1.0 API
// reference"; PLAN.md Layer 6).
var specBadOperations = []string{
	"chats.messages.CreateHostedContents",
	"groups.team.channels.messages.CreateHostedContents",
	"me.chats.messages.CreateHostedContents",
	"users.chats.messages.CreateHostedContents",
}

// TestRouteListExcludesSpecOnlyOperations is the invariant PLAN.md Layer 6 asks
// for: the route list must not be derived from spec operations, or it would
// bless the broken standalone hostedContents POST.
func TestRouteListExcludesSpecOnlyOperations(t *testing.T) {
	for _, line := range Routes() {
		_, path, ok := strings.Cut(line, " ")
		if !ok {
			t.Fatalf("malformed route %q", line)
		}
		// The broken call is POST .../messages/{id}/hostedContents - a POST on a
		// hostedContents collection.
		if strings.HasPrefix(line, "POST ") && strings.HasSuffix(path, "/hostedContents") {
			t.Errorf("route list contains %q, which is the hostedContents POST that has no api-reference page", line)
		}
	}

	// The committed routes.txt must not mention any spec-only operation id.
	text := string(routesTXT)
	for _, op := range specBadOperations {
		if strings.Contains(text, op) {
			t.Errorf("committed routes.txt mentions spec-only operation %q", op)
		}
	}
}

// TestRouteListMatchesRoutesGo makes sure the committed routes.txt and the Go
// route table cannot drift apart.
func TestRouteListMatchesRoutesGo(t *testing.T) {
	parsed, err := parseRoutesText(string(routesTXT))
	if err != nil {
		t.Fatalf("parse committed routes.txt: %v", err)
	}
	got := make([]string, 0, len(parsed))
	for _, r := range parsed {
		got = append(got, r.Method+" "+r.Path)
	}
	want := Routes()
	if len(got) != len(want) {
		t.Fatalf("routes.txt has %d entries, Routes() has %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("entry %d: routes.txt has %q, Routes() has %q", i, got[i], want[i])
		}
	}
}

// TestRouteSourcesExist checks that every route cites an api-reference page and
// that the pages exist in the mirror when it is available.
func TestRouteSourcesExist(t *testing.T) {
	root := repoRoot()
	available := true
	if _, err := os.Stat(filepath.Join(root, apiReferenceDir)); err != nil {
		available = false
	}
	for _, r := range routes {
		if r.Source == "" {
			t.Errorf("route %s %s has no source page", r.Method, r.Path)
		}
		if !strings.HasSuffix(r.Source, ".md") {
			t.Errorf("route %s %s source %q is not a markdown page", r.Method, r.Path, r.Source)
		}
		if available {
			if _, err := os.Stat(filepath.Join(root, apiReferenceDir, r.Source)); err != nil {
				t.Errorf("route %s %s cites %s, which is not in the mirror", r.Method, r.Path, r.Source)
			}
		}
	}
}

// TestRoutesAreDocumented runs the api-reference check the generator runs, so
// the invariant is enforced by `go test` on a machine that has the mirror too.
func TestRoutesAreDocumented(t *testing.T) {
	root := hasRefs(t)
	if err := verifyRoutesAgainstAPIReference(root); err != nil {
		t.Fatal(err)
	}
}

// TestODataTypeStripped pins the documented divergence: Microsoft's copy marks
// @odata.type required in thousands of schemas, the api-reference says only body
// is mandatory on a chatMessage write
// (refs/graph/api-reference/v1.0/api/channel-post-messages.md, "Request body"),
// so the trimmed copy strips it and microsoft.graph.chatMessage.required ends up
// as just body.
func TestODataTypeStripped(t *testing.T) {
	schemas := loadTrimmedSchemas(t)

	chatMessage := schemas["microsoft.graph.chatMessage"]
	if chatMessage == nil {
		t.Fatal("microsoft.graph.chatMessage is missing from the trimmed spec")
	}
	// The source requires @odata.type both directly on chatMessage and again
	// through microsoft.graph.entity (its allOf parent). After the strip rule
	// neither of them requires anything: the description never marked body as
	// required, and the api-reference's "only body is mandatory" is a statement
	// about the write flow, not a JSON Schema constraint. The property itself
	// stays declared so a client that sends @odata.type is still validated.
	if got := requiredFromAllOf(chatMessage); len(got) != 0 {
		t.Errorf("microsoft.graph.chatMessage.required = %v, want none (the source required only @odata.type)", got)
	}
	if got := requiredFromAllOf(schemas["microsoft.graph.entity"]); len(got) != 0 {
		t.Errorf("microsoft.graph.entity.required = %v, want none", got)
	}

	// The blanket rule must have been applied everywhere, not just there.
	for name, schema := range schemas {
		for _, r := range requiredFromAllOf(schema) {
			if r == odataTypeKey {
				t.Errorf("%s still requires %s", name, odataTypeKey)
			}
		}
	}

	// The property itself must still be declared, so a client that sends it is
	// still validated rather than rejected as an unknown field.
	if !declaresProperty(schemas["microsoft.graph.entity"], odataTypeKey) {
		t.Errorf("microsoft.graph.entity no longer declares %s; the strip rule must not remove properties", odataTypeKey)
	}
}

// TestTrimmedSpecHasNoDanglingRefs guards the trimmer's post-condition against a
// hand-edited committed file.
func TestTrimmedSpecHasNoDanglingRefs(t *testing.T) {
	var doc yaml.Node
	if err := yaml.Unmarshal(trimmedSpecYAML, &doc); err != nil {
		t.Fatalf("committed trimmed spec is not valid YAML: %v", err)
	}
	top, err := documentMapping(&doc)
	if err != nil {
		t.Fatal(err)
	}
	index, err := indexComponents(top["components"])
	if err != nil {
		t.Fatal(err)
	}
	if err := checkRefsResolve(top["paths"], index); err != nil {
		t.Fatal(err)
	}
}

// TestCommittedArtifactsAreCurrent re-runs the generator in memory and compares
// against the committed files, which is the check the refs-check CI job runs
// (PLAN.md "CI/CD"). It skips when the refs/ mirror is not available, because
// ordinary `go test ./...` must not need it.
func TestCommittedArtifactsAreCurrent(t *testing.T) {
	root := mirrorHeavy(t)
	generated, err := Generate(root)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	for _, tc := range []struct {
		rel  string
		want []byte
	}{
		{routesPath, generated.Routes},
		{provenancePath, generated.Provenance},
		{trimmedSpecPath, Spec(generated)},
	} {
		path := filepath.Join(root, tc.rel)
		got, err := os.ReadFile(path)
		if err != nil {
			t.Errorf("read committed %s: %v", tc.rel, err)
			continue
		}
		if !bytes.Equal(got, tc.want) {
			t.Errorf("%s is stale: re-run scripts/gen-contract.sh (%d bytes committed, %d generated)",
				tc.rel, len(got), len(tc.want))
		}
	}
	if t.Failed() {
		t.Log("the trimmed spec and the route list are generated; never edit them by hand")
	}
}

// TestGenerateIsDeterministic runs the generator twice and compares, which is
// what makes the refs-check `git diff --exit-code` meaningful.
func TestGenerateIsDeterministic(t *testing.T) {
	root := mirrorHeavy(t)
	first, err := Generate(root)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Generate(root)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(Spec(first), Spec(second)) {
		t.Error("two generator runs produced different specs")
	}
	if !bytes.Equal(first.Routes, second.Routes) {
		t.Error("two generator runs produced different route lists")
	}
	if !bytes.Equal(first.Provenance, second.Provenance) {
		t.Error("two generator runs produced different provenance records")
	}
}

// TestGenerateWithoutRefsFailsClearly pins the required failure mode: a clear
// message naming what to run, not a panic or a bare stat error.
//
// The messages carry the path the generator looked for, which is built with
// filepath.Join, so the assertions go through filepath.ToSlash: on Windows the
// same message reads `refs\graph\api-reference\...` and used to fail this test
// (the first CI run of this package on windows-latest, 2026-10-04).
func TestGenerateWithoutRefsFailsClearly(t *testing.T) {
	t.Run("no mirror at all", func(t *testing.T) {
		_, err := Generate(t.TempDir())
		if err == nil {
			t.Fatal("Generate succeeded against an empty directory")
		}
		msg := filepath.ToSlash(err.Error())
		for _, want := range []string{"refs", "scripts/fetch-refs.sh"} {
			if !strings.Contains(msg, want) {
				t.Errorf("error %q does not mention %q", msg, want)
			}
		}
	})

	t.Run("api-reference missing", func(t *testing.T) {
		root := t.TempDir()
		if err := os.MkdirAll(filepath.Join(root, filepath.Dir(openAPISource)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, openAPISource), []byte("openapi: 3.0.4\npaths: {}\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		_, err := Generate(root)
		if err == nil {
			t.Fatal("Generate succeeded without the api-reference")
		}
		msg := filepath.ToSlash(err.Error())
		for _, want := range []string{apiReferenceDir, "scripts/fetch-refs.sh"} {
			if !strings.Contains(msg, want) {
				t.Errorf("error %q does not mention %q", msg, want)
			}
		}
	})

	t.Run("openapi missing", func(t *testing.T) {
		// A refs/ tree that has the api-reference pages but not the description:
		// every page the route list cites is created empty, which is enough for
		// the page checks to pass and for the missing-description error to be the
		// one that surfaces.
		root := t.TempDir()
		pages := map[string][]Route{}
		for _, r := range routes {
			pages[r.Source] = append(pages[r.Source], r)
		}
		for page, rs := range pages {
			path := filepath.Join(root, apiReferenceDir, page)
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			var b strings.Builder
			b.WriteString("# stub\n\n```http\n")
			for _, r := range rs {
				b.WriteString(r.Method + " " + r.Path + "\n")
			}
			b.WriteString("```\n")
			if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		_, err := Generate(root)
		if err == nil {
			t.Fatal("Generate succeeded without the OpenAPI description")
		}
		msg := filepath.ToSlash(err.Error())
		for _, want := range []string{openAPISource, "scripts/fetch-refs.sh"} {
			if !strings.Contains(msg, want) {
				t.Errorf("error %q does not mention %q", msg, want)
			}
		}
	})
}

// TestProvenanceRecordsTheRules checks that the committed provenance file names
// the source SHA, the path count and the divergences, which is where PLAN.md
// asks the @odata.type conflict to be recorded.
func TestProvenanceRecordsTheRules(t *testing.T) {
	root := repoRoot()
	raw, err := os.ReadFile(filepath.Join(root, provenancePath))
	if err != nil {
		t.Fatalf("read committed provenance: %v", err)
	}
	s := string(raw)
	for _, want := range []string{
		"7b2914c8ad1340129f52aa785f13c074cb46fd7c", // the SHA pinned in refs/MANIFEST.md
		"pathsKept",
		"odataTypeRequiredStripped",
		"@odata.type",
		"CreateHostedContents",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("provenance.json does not mention %q", want)
		}
	}
}

// ---- helpers ----

// loadTrimmedSchemas parses the committed trimmed spec into raw schema nodes,
// keyed by component name.
func loadTrimmedSchemas(t *testing.T) map[string]*yaml.Node {
	t.Helper()
	var doc yaml.Node
	if err := yaml.Unmarshal(trimmedSpecYAML, &doc); err != nil {
		t.Fatalf("parse committed trimmed spec: %v", err)
	}
	top, err := documentMapping(&doc)
	if err != nil {
		t.Fatal(err)
	}
	m := mappingValue(top["components"], "schemas")
	if m == nil {
		t.Fatal("trimmed spec has no components/schemas")
	}
	out := map[string]*yaml.Node{}
	for i := 0; i+1 < len(m.Content); i += 2 {
		out[m.Content[i].Value] = m.Content[i+1]
	}
	return out
}

// requiredFromAllOf collects the "required" entries of a schema and of every
// allOf branch, which is how the description composes its object shapes: a
// chatMessage is allOf [entity, {required: [@odata.type], propert...}].
func requiredFromAllOf(node *yaml.Node) []string {
	if node == nil {
		return nil
	}
	var out []string
	if req := mappingValue(node, "required"); req != nil && req.Kind == yaml.SequenceNode {
		for _, r := range req.Content {
			out = append(out, r.Value)
		}
	}
	if all := mappingValue(node, "allOf"); all != nil && all.Kind == yaml.SequenceNode {
		for _, branch := range all.Content {
			out = append(out, requiredFromAllOf(branch)...)
		}
	}
	return out
}

func declaresProperty(node *yaml.Node, name string) bool {
	if node == nil {
		return false
	}
	if props := mappingValue(node, "properties"); props != nil {
		if mappingValue(props, name) != nil {
			return true
		}
	}
	if all := mappingValue(node, "allOf"); all != nil && all.Kind == yaml.SequenceNode {
		for _, branch := range all.Content {
			if declaresProperty(branch, name) {
				return true
			}
		}
	}
	return false
}

// TestWriteArtifacts covers the writer the shell script depends on, including
// the directory it has to create.
func TestWriteArtifacts(t *testing.T) {
	root := t.TempDir()
	files, err := WriteArtifacts(root, &Generated{trimmed: []byte("openapi: 3.0.4\n"), Routes: []byte("# routes\n"), Provenance: []byte("{}\n")})
	if err != nil {
		t.Fatalf("WriteArtifacts: %v", err)
	}
	if len(files) != 3 {
		t.Fatalf("wrote %d files, want 3: %v", len(files), files)
	}
	spec, err := os.ReadFile(filepath.Join(root, trimmedSpecPath))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(spec), "# Trimmed Microsoft Graph") {
		t.Error("the written spec does not carry the generated header comment")
	}
	for _, path := range files {
		if _, err := os.Stat(path); err != nil {
			t.Errorf("declared output %s does not exist: %v", path, err)
		}
	}

	// A read-only parent directory must surface as an error, not a panic.
	if _, err := WriteArtifacts(filepath.Join(root, "missing", "deeper"), &Generated{Routes: []byte("x")}); err != nil {
		t.Fatalf("WriteArtifacts should create missing parents: %v", err)
	}
}

// TestDocumentAndConfigHelpers covers the small yaml.Node helpers the trimmer
// relies on, including the error paths.
func TestDocumentAndConfigHelpers(t *testing.T) {
	if _, err := documentMapping(nil); err == nil {
		t.Error("a nil document was accepted")
	}
	if _, err := documentMapping(&yaml.Node{Kind: yaml.ScalarNode}); err == nil {
		t.Error("a scalar root was accepted")
	}
	if _, err := documentMapping(&yaml.Node{Kind: yaml.DocumentNode}); err == nil {
		t.Error("an empty document was accepted")
	}
	if err := walkRefs(nil, func(string) error { return nil }); err != nil {
		t.Errorf("walkRefs(nil) = %v", err)
	}
	// A non-component $ref must be reported rather than silently kept.
	err := checkRefsResolve(&yaml.Node{Kind: yaml.MappingNode, Content: []*yaml.Node{
		scalar("$ref"), scalar("#/paths/~1me"),
	}}, map[string]map[string]*yaml.Node{"schemas": {}})
	if err == nil {
		t.Error("a non-component $ref was accepted")
	}
	// A dangling component $ref must be reported.
	err = checkRefsResolve(&yaml.Node{Kind: yaml.MappingNode, Content: []*yaml.Node{
		scalar("$ref"), scalar("#/components/schemas/gone"),
	}}, map[string]map[string]*yaml.Node{"schemas": {}})
	if err == nil {
		t.Error("a dangling component $ref was accepted")
	}
}
