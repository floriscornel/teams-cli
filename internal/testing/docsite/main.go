// Command docsite renders the user documentation into a static site.
//
// Input: README.md (the front page) and docs/commands/*.md (the command
// reference that docgen generates from the cobra tree). Output: an index page
// and one page per command, with the stylesheet inlined and no external assets,
// so the same directory works on GitHub Pages, behind any static host, or
// straight from the filesystem.
//
// It is developer tooling like docgen: `mise run docs-site` builds the site into
// dist/ (never committed), and the "docs site" workflow publishes that directory
// with actions/deploy-pages. Nothing here needs Node, Hugo or a theme.
package main

import (
	"bytes"
	"flag"
	"fmt"
	"html/template"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
)

// repoURL is where links to files that are not part of the site go.
const repoURL = "https://github.com/floriscornel/teams-cli"

// page is one rendered document.
type page struct {
	// File is the output file name ("index.html", "teams_post.html").
	File string
	// Title is what the sidebar and the <title> show.
	Title string
	// Usage is the command line a reference page documents ("teams post"), when
	// the page is one.
	Usage string
	// Body is the rendered HTML.
	Body template.HTML
	// Section groups the sidebar: "Guide" or "Commands".
	Section string
}

func main() {
	out := flag.String("out", filepath.Join("dist", "docs-site"), "directory to write the site into")
	root := flag.String("root", ".", "repository root to read the sources from")
	flag.Parse()

	site, err := buildSite(*root)
	if err != nil {
		fail(err)
	}
	if err := os.MkdirAll(*out, 0o755); err != nil {
		fail(err)
	}
	for _, p := range site {
		if err := writePage(*out, p, site); err != nil {
			fail(err)
		}
	}
	// .nojekyll keeps GitHub Pages from running Jekyll over the output; there is
	// no index of files that needs it today, and it costs nothing.
	if err := os.WriteFile(filepath.Join(*out, ".nojekyll"), nil, 0o644); err != nil { //nolint:gosec // a generated marker file
		fail(err)
	}
	_, _ = fmt.Fprintf(os.Stdout, "wrote %d pages to %s\n", len(site), *out)
}

// buildSite renders every page.
func buildSite(root string) ([]page, error) {
	readme, err := os.ReadFile(filepath.Join(root, "README.md"))
	if err != nil {
		return nil, err
	}
	index, err := render(docLinkRewriter(string(readme), root))
	if err != nil {
		return nil, err
	}
	site := []page{{
		File:    "index.html",
		Title:   "teams — Microsoft Teams from the command line",
		Body:    index,
		Section: "Guide",
	}}

	commandsDir := filepath.Join(root, "docs", "commands")
	entries, err := os.ReadDir(commandsDir)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w (run `mise run docs` first)", commandsDir, err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".md") {
			names = append(names, entry.Name())
		}
	}
	sort.Strings(names)
	for _, name := range names {
		data, err := os.ReadFile(filepath.Join(commandsDir, name))
		if err != nil {
			return nil, err
		}
		body, err := render(commandLinkRewriter(string(data)))
		if err != nil {
			return nil, err
		}
		site = append(site, page{
			File:    strings.TrimSuffix(name, ".md") + ".html",
			Title:   firstHeading(string(data)),
			Usage:   firstHeading(string(data)),
			Body:    body,
			Section: "Commands",
		})
	}
	return site, nil
}

// render converts markdown to HTML. Raw HTML in the source is escaped rather
// than passed through (goldmark's default), so nothing in the docs can inject
// markup into the site.
func render(markdown string) (template.HTML, error) {
	var buf bytes.Buffer
	md := goldmark.New(goldmark.WithExtensions(extension.GFM))
	if err := md.Convert([]byte(markdown), &buf); err != nil {
		return "", err
	}
	return template.HTML(buf.String()), nil //nolint:gosec // goldmark escapes raw HTML in the source
}

// markdownLink matches a markdown link's target, including the brackets: the
// match is "](" + target + ")", so a caller takes match[2:len(match)-1].
var markdownLink = regexp.MustCompile(`\]\(([^)\s]+)\)`)

// linkTarget returns the target of a markdownLink match.
func linkTarget(match string) string { return match[2 : len(match)-1] }

// commandLinkRewriter points a command page's links at its sibling pages: cobra
// writes "SEE ALSO" links as teams_alias.md.
func commandLinkRewriter(markdown string) string {
	return markdownLink.ReplaceAllStringFunc(markdown, func(match string) string {
		target := linkTarget(match)
		if strings.HasSuffix(target, ".md") && !strings.Contains(target, "/") {
			return "](" + strings.TrimSuffix(target, ".md") + ".html)"
		}
		return match
	})
}

// docLinkRewriter points the README's links at the site or at GitHub: a link to
// a command page becomes the page, everything else (PLAN.md, AGENTS.md,
// docs/ci.md, refs/…, LICENSE) becomes a link into the repository, because the
// site does not carry those files.
func docLinkRewriter(markdown, root string) string {
	return markdownLink.ReplaceAllStringFunc(markdown, func(match string) string {
		raw := linkTarget(match)
		target, anchor, _ := strings.Cut(raw, "#")
		if target == "" || strings.HasPrefix(target, "http://") || strings.HasPrefix(target, "https://") || strings.HasPrefix(target, "mailto:") {
			return match
		}
		// A link to a generated command page is a link inside the site.
		if page, ok := sitePageFor(root, target); ok {
			if anchor != "" {
				return "](" + page + "#" + anchor + ")"
			}
			return "](" + page + ")"
		}
		url := repoURL + "/blob/main/" + strings.TrimPrefix(target, "./")
		if anchor != "" {
			url += "#" + anchor
		}
		return "](" + url + ")"
	})
}

// sitePageFor maps a repository-relative path to a page in the site, when it has
// one.
func sitePageFor(root, target string) (string, bool) {
	cleaned := strings.TrimPrefix(filepath.ToSlash(target), "./")
	if cleaned == "README.md" {
		return "index.html", true
	}
	if name, ok := strings.CutPrefix(cleaned, "docs/commands/"); ok && strings.HasSuffix(name, ".md") {
		if _, err := os.Stat(filepath.Join(root, "docs", "commands", name)); err == nil {
			return strings.TrimSuffix(name, ".md") + ".html", true
		}
	}
	return "", false
}

// firstHeading returns the text of the first heading of any level, which is what
// cobra writes at the top of a command page (cobra uses "## teams post", so a
// level-1-only search would find nothing).
func firstHeading(markdown string) string {
	for line := range strings.SplitSeq(markdown, "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "#") {
			continue
		}
		text := strings.TrimSpace(strings.TrimLeft(trimmed, "#"))
		if text != "" {
			return text
		}
	}
	return ""
}

// writePage renders one page through the shared layout.
func writePage(dir string, p page, site []page) error {
	file, err := os.Create(filepath.Join(dir, p.File)) //nolint:gosec // a generated file in the output directory
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()
	return pageTemplate.Execute(file, struct {
		Page  page
		Pages []page
	}{Page: p, Pages: site})
}

// pageTemplate is the whole site chrome: a sidebar with every page, the document,
// and an inline stylesheet. It is one template because the site is one page
// shape.
var pageTemplate = template.Must(template.New("page").Parse(`<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>{{ .Page.Title }}</title>
<style>` + stylesheet + `</style>
</head>
<body>
<nav>
  <p class="brand"><a href="index.html">teams</a></p>
  {{ range .Pages }}{{ if eq .Section "Guide" }}<p class="section">Guide</p><ul>
    <li><a href="{{ .File }}">Overview</a></li>
  </ul>{{ end }}{{ end }}
  <p class="section">Command reference</p>
  <ul>
  {{ range .Pages }}{{ if eq .Section "Commands" }}<li><a href="{{ .File }}">{{ .Usage }}</a></li>{{ end }}{{ end }}
  </ul>
  <p class="section">Repository</p>
  <ul>
    <li><a href="` + repoURL + `">Source and issues</a></li>
    <li><a href="` + repoURL + `/blob/main/PLAN.md">Design (PLAN.md)</a></li>
  </ul>
</nav>
<main>
{{ .Page.Body }}
<footer>Generated from the repository's own documentation. The design notes live in
<a href="` + repoURL + `/blob/main/PLAN.md">PLAN.md</a>.</footer>
</main>
</body>
</html>
`))

// stylesheet is deliberately small and dependency-free: system fonts, a readable
// measure, and code blocks that do not overflow.
const stylesheet = `
:root { color-scheme: light dark; }
* { box-sizing: border-box; }
body { margin: 0; display: flex; font: 16px/1.6 -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, Helvetica, Arial, sans-serif; }
nav { flex: 0 0 16rem; padding: 1.5rem 1rem; border-right: 1px solid #8883; position: sticky; top: 0; height: 100vh; overflow-y: auto; }
nav .brand { font-weight: 700; font-size: 1.1rem; margin: 0 0 1rem; }
nav .brand a { text-decoration: none; }
nav .section { font-size: .75rem; text-transform: uppercase; letter-spacing: .08em; opacity: .6; margin: 1.2rem 0 .3rem; }
nav ul { list-style: none; margin: 0; padding: 0; }
nav li { margin: .15rem 0; }
nav a { text-decoration: none; font-size: .9rem; }
nav a:hover { text-decoration: underline; }
main { flex: 1 1 auto; max-width: 52rem; padding: 2rem 2.5rem 4rem; }
h1, h2, h3 { line-height: 1.25; }
h1 { font-size: 1.7rem; } h2 { font-size: 1.3rem; margin-top: 2rem; } h3 { font-size: 1.05rem; }
code, pre { font-family: ui-monospace, SFMono-Regular, "SF Mono", Menlo, Consolas, monospace; font-size: .88em; }
code { background: #8881; padding: .1em .35em; border-radius: 4px; }
pre { background: #8881; padding: .8rem 1rem; border-radius: 8px; overflow-x: auto; }
pre code { background: none; padding: 0; }
table { border-collapse: collapse; width: 100%; margin: 1rem 0; }
th, td { border: 1px solid #8883; padding: .35rem .6rem; text-align: left; vertical-align: top; }
th { background: #8881; }
blockquote { margin: 1rem 0; padding: 0 1rem; border-left: 3px solid #8885; opacity: .9; }
footer { margin-top: 3rem; padding-top: 1rem; border-top: 1px solid #8883; font-size: .85rem; opacity: .8; }
@media (max-width: 50rem) { body { flex-direction: column; } nav { position: static; height: auto; border-right: none; border-bottom: 1px solid #8883; } main { padding: 1.5rem; } }
`

func fail(err error) {
	_, _ = fmt.Fprintf(os.Stderr, "docsite: %v\n", err)
	os.Exit(1)
}
