package contract

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// openAPISource is the gitignored mirror of Microsoft's Graph v1.0 OpenAPI
// description (refs/INDEX.md, "Mirror layout"; PLAN.md Layer 6). It is ~44 MB
// with ~11,500 path keys and zero external $refs.
const openAPISource = "refs/openapi/openapi/v1.0/openapi.yaml"

// Committed output locations, relative to the repository root.
const (
	trimmedSpecPath = "internal/testing/testdata/openapi/graph-v1.0-trimmed.yaml"
	routesPath      = "internal/testing/testdata/openapi/routes.txt"
	provenancePath  = "internal/testing/testdata/openapi/provenance.json"
)

// Generated is the in-memory result of a generator run. The currency test
// compares it against what is committed, so nothing here may depend on the
// clock, the filesystem order or the environment.
type Generated struct {
	trimmed    []byte
	Routes     []byte
	Provenance []byte
}

// Spec returns the trimmed OpenAPI document including its header comment.
func Spec(g *Generated) []byte {
	return append([]byte(headerComment), g.trimmed...)
}

// Generate reads the OpenAPI mirror below root and returns the trimmed spec,
// the route list and the provenance record. It performs no file writes, so a
// test can call it directly.
//
// When refs/ is missing the error says exactly what to run, because the mirror
// is gitignored and a fresh checkout never has it.
func Generate(root string) (*Generated, error) {
	if err := requireMirror(root); err != nil {
		return nil, err
	}
	if err := verifyRoutesAgainstAPIReference(root); err != nil {
		return nil, err
	}
	source := filepath.Join(root, openAPISource)
	raw, err := os.ReadFile(source)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, errMissingMirror(source)
		}
		return nil, fmt.Errorf("contract: read %s: %w", source, err)
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("contract: parse %s: %w", source, err)
	}

	trimmed, stats, err := trimDocument(&doc)
	if err != nil {
		return nil, err
	}
	spec, err := marshalYAML(trimmed)
	if err != nil {
		return nil, err
	}
	// Validate the trimmed document before it can be committed, so a malformed
	// trim fails here rather than in the first contract test.
	if _, err := newValidator(spec, []byte(routesText())); err != nil {
		return nil, fmt.Errorf("contract: the trimmed document does not load: %w", err)
	}
	prov, err := provenanceJSON(root, stats)
	if err != nil {
		return nil, err
	}
	return &Generated{trimmed: spec, Routes: []byte(routesText()), Provenance: prov}, nil
}

// requireMirror fails early, with one clear message, when the gitignored
// reference mirror has not been fetched. Everything the generator reads lives
// under refs/ (AGENTS.md).
func requireMirror(root string) error {
	dir := filepath.Join(root, "refs")
	info, err := os.Stat(dir)
	switch {
	case err != nil:
		return errMissingMirror(dir)
	case !info.IsDir():
		return errMissingMirror(dir)
	}
	return nil
}

// errMissingMirror is the single, actionable "fetch the mirror" error.
func errMissingMirror(path string) error {
	return fmt.Errorf(
		"contract: %s is missing; the reference mirror is gitignored, so run scripts/fetch-refs.sh first "+
			"(it reproduces refs/ at the SHAs pinned in refs/MANIFEST.md)",
		path)
}

// stats records what the trimmer did, for the provenance file and the report.
type stats struct {
	pathKeysInSource    int
	pathsKept           int
	schemasKept         int
	responsesKept       int
	paramsKept          int
	bodiesKept          int
	odataStrip          int
	aliasRewrites       int
	discriminatorPruned int
}

// trimDocument builds the trimmed document node from the parsed source.
func trimDocument(root *yaml.Node) (*yaml.Node, stats, error) {
	var st stats
	top, err := documentMapping(root)
	if err != nil {
		return nil, st, err
	}
	paths := top["paths"]
	components := top["components"]
	if paths == nil || components == nil {
		return nil, st, fmt.Errorf("contract: source document has no paths/components")
	}
	index, err := indexComponents(components)
	if err != nil {
		return nil, st, err
	}

	// ---- paths: one spec operation per committed route ----
	byPath := map[string][]resolved{}
	for _, r := range routes {
		c, err := findOperation(paths, r)
		if err != nil {
			return nil, st, err
		}
		// The trimmed key is the api-reference spelling, because that is the
		// path internal/graph sends and the whole point of the trimmed copy is
		// to validate what we send. Microsoft spells several endpoints
		// differently in the two sources (/groups/{group-id}/team/channels/...
		// versus /teams/{team-id}/channels/...), so the operation is re-keyed
		// and its path parameters are renamed to match the new key.
		key := r.Path
		// Two routes can land on the same trimmed path (for example the GET on
		// .../hostedContents/{id} and the GET on .../hostedContents, which the
		// description resolves to different paths but the api-reference spells
		// as one). Keep the first occurrence per method so the emitted path item
		// stays valid YAML with unique keys.
		if hasMethod(byPath[key], strings.ToLower(r.Method)) {
			continue
		}
		byPath[key] = append(byPath[key], resolved{
			method: strings.ToLower(r.Method),
			op:     c.op,
			params: c.params,
			alias:  c.alias,
		})
	}
	pathNodes := map[string]*yaml.Node{}
	for _, specPath := range sortedKeysList(byPath) {
		item := &yaml.Node{Kind: yaml.MappingNode}
		for _, res := range byPath[specPath] {
			item.Content = append(item.Content, scalar(res.method), res.op)
		}
		for _, res := range byPath[specPath] {
			if res.params == nil {
				continue
			}
			if len(res.alias) > 0 {
				renamePathParameters(res.params, res.alias)
				st.aliasRewrites++
			}
			item.Content = append(item.Content, scalar("parameters"), res.params)
			break
		}
		pathNodes[specPath] = item
	}
	st.pathKeysInSource = len(paths.Content) / 2

	// ---- $ref closure over components ----
	keep := map[string]map[string]bool{
		"schemas": {}, "responses": {}, "parameters": {}, "requestBodies": {},
	}
	type item struct{ category, name string }
	var queue []item
	visit := func(ref string) error {
		cat, name, ok := splitComponentRef(ref)
		if !ok {
			return fmt.Errorf("contract: unexpected $ref %q (expected #/components/<category>/<name>)", ref)
		}
		if _, known := keep[cat]; !known {
			return fmt.Errorf("contract: $ref %q points at an unsupported component category %q", ref, cat)
		}
		if keep[cat][name] {
			return nil
		}
		keep[cat][name] = true
		queue = append(queue, item{cat, name})
		return nil
	}
	for _, name := range sortedKeys(pathNodes) {
		if err := walkRefs(pathNodes[name], visit); err != nil {
			return nil, st, err
		}
	}
	for len(queue) > 0 {
		it := queue[0]
		queue = queue[1:]
		node := index[it.category][it.name]
		if node == nil {
			return nil, st, fmt.Errorf("contract: $ref '#/components/%s/%s' cannot be resolved in the source document", it.category, it.name)
		}
		if err := walkRefs(node, visit); err != nil {
			return nil, st, err
		}
	}

	// ---- repair rules (see repair.go) ----
	st.odataStrip, err = stripODataTypeRequired(pathNodes, keep, index)
	if err != nil {
		return nil, st, err
	}
	st.discriminatorPruned, err = pruneDiscriminatorMappings(pathNodes, keep, index)
	if err != nil {
		return nil, st, err
	}

	// ---- assemble ----
	doc := &yaml.Node{Kind: yaml.DocumentNode}
	m := &yaml.Node{Kind: yaml.MappingNode}
	doc.Content = []*yaml.Node{m}
	for _, key := range []string{"openapi", "info", "servers"} {
		if n := top[key]; n != nil {
			m.Content = append(m.Content, scalar(key), n)
		}
	}
	pm := &yaml.Node{Kind: yaml.MappingNode}
	for _, name := range sortedKeys(pathNodes) {
		pm.Content = append(pm.Content, scalar(name), pathNodes[name])
	}
	st.pathsKept = len(pathNodes)
	m.Content = append(m.Content, scalar("paths"), pm)

	cm := &yaml.Node{Kind: yaml.MappingNode}
	for _, cat := range []string{"schemas", "responses", "parameters", "requestBodies"} {
		names := sortedKeys2(keep[cat])
		if len(names) == 0 {
			continue
		}
		sub := &yaml.Node{Kind: yaml.MappingNode}
		for _, name := range names {
			sub.Content = append(sub.Content, scalar(name), index[cat][name])
		}
		cm.Content = append(cm.Content, scalar(cat), sub)
		switch cat {
		case "schemas":
			st.schemasKept = len(names)
		case "responses":
			st.responsesKept = len(names)
		case "parameters":
			st.paramsKept = len(names)
		case "requestBodies":
			st.bodiesKept = len(names)
		}
	}
	m.Content = append(m.Content, scalar("components"), cm)

	// ---- post-conditions ----
	if err := checkRefsResolve(doc, index); err != nil {
		return nil, st, err
	}
	return doc, st, nil
}

// documentMapping returns the top-level mapping keyed by its keys.
func documentMapping(root *yaml.Node) (map[string]*yaml.Node, error) {
	if root == nil || root.Kind != yaml.DocumentNode || len(root.Content) == 0 {
		return nil, fmt.Errorf("contract: source document is not a YAML document")
	}
	body := root.Content[0]
	if body.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("contract: source document root is not a mapping")
	}
	out := map[string]*yaml.Node{}
	for i := 0; i+1 < len(body.Content); i += 2 {
		out[body.Content[i].Value] = body.Content[i+1]
	}
	return out, nil
}

// indexComponents maps category -> name -> node.
func indexComponents(components *yaml.Node) (map[string]map[string]*yaml.Node, error) {
	if components.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("contract: components is not a mapping")
	}
	out := map[string]map[string]*yaml.Node{}
	for i := 0; i+1 < len(components.Content); i += 2 {
		cat := components.Content[i].Value
		sub := components.Content[i+1]
		m := map[string]*yaml.Node{}
		if sub.Kind == yaml.MappingNode {
			for j := 0; j+1 < len(sub.Content); j += 2 {
				m[sub.Content[j].Value] = sub.Content[j+1]
			}
		}
		out[cat] = m
	}
	return out, nil
}

// opMatch is a spec operation that implements one committed route.
type opMatch struct {
	path   string
	method string
	op     *yaml.Node
	params *yaml.Node
	alias  map[string]string
}

// findOperation locates the spec operation that implements one committed route.
//
// It matches on (method, path shape) – parameter names differ between the
// api-reference and the description – and then confirms the match with the
// operation's externalDocs URL, which points at the api-reference page the
// route came from. That second check is what keeps the trimmer honest: an
// operation Microsoft ships without an api-reference page (CreateHostedContents)
// has no externalDocs, so it can never be pulled in by accident.
func findOperation(paths *yaml.Node, r Route) (opMatch, error) {
	var zero opMatch
	want := pathShape(r.Path)
	var matches []string
	for i := 0; i+1 < len(paths.Content); i += 2 {
		specPath := paths.Content[i].Value
		if pathShape(specPath) != want {
			continue
		}
		matches = append(matches, specPath)
	}
	if len(matches) == 0 {
		return zero, fmt.Errorf(
			"contract: %s %s (shape %s) has no matching path in %s; "+
				"the API surface changed or the route is wrong",
			r.Method, r.Path, want, openAPISource)
	}
	sort.Strings(matches)

	var cands []opMatch
	for _, specPath := range matches {
		idx := indexOfPath(paths, specPath)
		if idx < 0 {
			continue
		}
		item := paths.Content[idx]
		method := strings.ToLower(r.Method)
		op := mappingValue(item, method)
		if op == nil {
			continue
		}
		cands = append(cands, opMatch{
			path:   specPath,
			method: method,
			op:     op,
			params: mappingValue(item, "parameters"),
			alias:  aliasParameters(specPath, r.Path),
		})
	}
	if len(cands) == 0 {
		return zero, fmt.Errorf(
			"contract: %s %s is documented in the api-reference but %s declares no %s operation on any of %v",
			r.Method, r.Path, openAPISource, r.Method, matches)
	}
	page := strings.TrimSuffix(r.Source, ".md")
	var documented []opMatch
	for _, c := range cands {
		if externalDocsPage(c.op) == page {
			documented = append(documented, c)
		}
	}
	switch {
	case len(documented) == 1:
		return documented[0], nil
	case len(documented) > 1:
		return zero, fmt.Errorf(
			"contract: %s %s matches several operations whose externalDocs point at %s (%v); "+
				"make the route unambiguous", r.Method, r.Path, r.Source, matchPaths(documented))
	case len(cands) == 1:
		// The operation's externalDocs do not name the page the route list
		// cites, which happens when Microsoft links an operation to a sibling
		// page (the hosted-content byte fetch is documented on
		// chatmessagehostedcontent-get.md but linked to
		// chatmessage-list-hostedcontents.md) or omits externalDocs entirely.
		// verifyRoutesAgainstAPIReference has already proved the route is
		// documented on its own page, so a unique shape match is accepted.
		return cands[0], nil
	default:
		return zero, fmt.Errorf(
			"contract: %s %s is ambiguous: %v match the shape and none points at %s; "+
				"make the route unambiguous",
			r.Method, r.Path, matchPaths(cands), r.Source)
	}
}

func matchPaths(cs []opMatch) []string {
	out := make([]string, 0, len(cs))
	for _, c := range cs {
		out = append(out, c.path)
	}
	return out
}

// indexOfPath returns the index of a path key in the paths mapping, or -1.
func indexOfPath(paths *yaml.Node, name string) int {
	for i := 0; i+1 < len(paths.Content); i += 2 {
		if paths.Content[i].Value == name {
			return i + 1
		}
	}
	return -1
}

// externalDocsPage returns the api-reference page name an operation links to,
// without the .md suffix, or "" when the operation has no externalDocs.
func externalDocsPage(op *yaml.Node) string {
	docs := mappingValue(op, "externalDocs")
	if docs == nil {
		return ""
	}
	url := mappingValue(docs, "url")
	if url == nil {
		return ""
	}
	i := strings.Index(url.Value, "/graph/api/")
	if i < 0 {
		return ""
	}
	name := url.Value[i+len("/graph/api/"):]
	if j := strings.IndexAny(name, "?#"); j >= 0 {
		name = name[:j]
	}
	return name
}

// aliasParameters returns the path-parameter renames needed for a path item
// whose parameters came from specPath but whose key is now apiPath.
//
// It only handles the case where the two paths have the same number of
// segments; when the api-reference folds or drops segments (as it does for
// "/teams/{team-id}" against "/groups/{group-id}/team") there is no name to
// rename, and renamingPathParameters is not needed either.
func aliasParameters(specPath, apiPath string) map[string]string {
	spec := strings.Split(specPath, "/")
	api := strings.Split(apiPath, "/")
	if len(spec) != len(api) {
		return nil
	}
	out := map[string]string{}
	conflict := map[string]string{}
	for i := range spec {
		s, a := spec[i], api[i]
		if s == a || !isParamSegment(s) || !isParamSegment(a) {
			// Literal segments are handled by using the api-reference path as
			// the trimmed key (see trimDocument), not by a rename.
			continue
		}
		old := strings.Trim(s, "{}")
		renamed := strings.Trim(a, "{}")
		if prev, seen := conflict[old]; seen && prev != renamed {
			delete(out, old) // ambiguous: leave it alone
			continue
		}
		conflict[old] = renamed
		out[old] = renamed
	}
	return out
}

// isParamSegment reports whether a path segment is a parameter placeholder, in
// either the raw "{team-id}" spelling or the normalized "{}" shape.
func isParamSegment(s string) bool {
	return strings.HasPrefix(s, "{") && strings.HasSuffix(s, "}")
}

// renamePathParameters rewrites path parameter names in a path item's own
// "parameters" list so they match the path template the CLI actually sends.
// Without this, kin-openapi would reject a request whose path has no parameter
// named "{group-id}" in it.
func renamePathParameters(params *yaml.Node, aliases map[string]string) {
	if params == nil || params.Kind != yaml.SequenceNode {
		return
	}
	for _, p := range params.Content {
		name := mappingValue(p, "name")
		in := mappingValue(p, "in")
		if name == nil || in == nil || in.Value != "path" {
			continue
		}
		if newName, ok := aliases[name.Value]; ok {
			name.Value = newName
		}
	}
}

// splitComponentRef splits "#/components/schemas/foo" into ("schemas", "foo").
func splitComponentRef(ref string) (string, string, bool) {
	const prefix = "#/components/"
	if !strings.HasPrefix(ref, prefix) {
		return "", "", false
	}
	rest := strings.TrimPrefix(ref, prefix)
	cat, name, ok := strings.Cut(rest, "/")
	if !ok || cat == "" || name == "" {
		return "", "", false
	}
	return cat, name, true
}

// walkRefs calls visit for every "$ref" string in the subtree.
func walkRefs(n *yaml.Node, visit func(string) error) error {
	if n == nil {
		return nil
	}
	if n.Kind == yaml.MappingNode {
		for i := 0; i+1 < len(n.Content); i += 2 {
			if n.Content[i].Value == "$ref" {
				if err := visit(n.Content[i+1].Value); err != nil {
					return err
				}
			}
			if err := walkRefs(n.Content[i+1], visit); err != nil {
				return err
			}
		}
		return nil
	}
	for _, c := range n.Content {
		if err := walkRefs(c, visit); err != nil {
			return err
		}
	}
	return nil
}

// checkRefsResolve asserts that every $ref left in the trimmed document still
// points at a component that was kept. Without this the loader would fail much
// later with a much worse error.
func checkRefsResolve(doc *yaml.Node, index map[string]map[string]*yaml.Node) error {
	return walkRefs(doc, func(ref string) error {
		cat, name, ok := splitComponentRef(ref)
		if !ok {
			return fmt.Errorf("contract: trimmed document contains non-component $ref %q", ref)
		}
		if index[cat][name] == nil {
			return fmt.Errorf("contract: trimmed document contains dangling $ref %q", ref)
		}
		return nil
	})
}

// ---- small yaml.Node helpers ----

func scalar(v string) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Value: v}
}

func mappingValue(m *yaml.Node, key string) *yaml.Node {
	if m == nil || m.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i+1]
		}
	}
	return nil
}

func sortedKeys(m map[string]*yaml.Node) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func sortedKeys2(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func sortedKeysList[T any](m map[string][]T) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// resolved is one retained operation plus the path-item metadata it needs.
type resolved struct {
	method string
	op     *yaml.Node
	params *yaml.Node // the path item's own parameters, if any
	alias  map[string]string
}

// hasMethod reports whether a resolved group already carries a method.
func hasMethod(rs []resolved, method string) bool {
	for _, r := range rs {
		if r.method == method {
			return true
		}
	}
	return false
}

// marshalYAML renders the trimmed document deterministically: block style for
// everything, 2-space indent, sequences indented under their parent key (the
// style of the source document), and no flow collections.
func marshalYAML(doc *yaml.Node) ([]byte, error) {
	applyStyle(doc)
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(doc); err != nil {
		return nil, fmt.Errorf("contract: encode trimmed spec: %w", err)
	}
	if err := enc.Close(); err != nil {
		return nil, fmt.Errorf("contract: encode trimmed spec: %w", err)
	}
	return buf.Bytes(), nil
}

func applyStyle(n *yaml.Node) {
	switch n.Kind {
	case yaml.MappingNode:
		n.Style = 0
	case yaml.SequenceNode:
		n.Style = 0
	}
	for _, c := range n.Content {
		applyStyle(c)
	}
}

// headerComment is prepended to the trimmed spec. YAML comments survive a
// re-parse, so this stays byte-stable across generator runs.
const headerComment = `# Trimmed Microsoft Graph v1.0 OpenAPI description – generated, do not edit.
#
# Generated by internal/testing/contract (scripts/gen-contract.sh) from
# refs/openapi/openapi/v1.0/openapi.yaml, pinned to the SHA in refs/MANIFEST.md.
# The full description is a gitignored mirror; this file is committed so the
# contract tests need no network and no refs/ checkout (PLAN.md Layer 6).
#
# See provenance.json for the trim rules and README.md for the divergences
# between this copy and Microsoft's original.
`

// WriteArtifacts writes the generated files below root and returns their paths.
func WriteArtifacts(root string, g *Generated) ([]string, error) {
	files := []struct {
		rel  string
		data []byte
	}{
		{trimmedSpecPath, Spec(g)},
		{routesPath, g.Routes},
		{provenancePath, g.Provenance},
	}
	var written []string
	for _, f := range files {
		path := filepath.Join(root, f.rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return written, fmt.Errorf("contract: %w", err)
		}
		if err := os.WriteFile(path, f.data, 0o644); err != nil {
			return written, fmt.Errorf("contract: write %s: %w", path, err)
		}
		written = append(written, path)
	}
	return written, nil
}

// odataTypeKey is the property Microsoft over-marks as required.
const odataTypeKey = "@odata.type"
