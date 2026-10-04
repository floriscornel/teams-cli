package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The site is generated from the repository's own markdown, so these tests pin
// the two things that decide whether a page is usable: its title (the sidebar
// entry) and where its links point.

func TestGuidePagesRender(t *testing.T) {
	root := filepath.Join("..", "..", "..")
	guide := filepath.Join(root, "docs", "guides", "app-registration.md")
	if _, err := os.Stat(guide); err != nil {
		t.Skipf("no guide to render: %v", err)
	}
	site, err := buildSite(root)
	if err != nil {
		t.Fatalf("buildSite: %v", err)
	}
	var found *page
	for i := range site {
		if site[i].File == "guide-app-registration.html" {
			found = &site[i]
		}
	}
	if found == nil {
		t.Fatal("the guide did not become a page")
	}
	if found.Title != "Register your own Entra app (recommended)" {
		t.Errorf("title = %q", found.Title)
	}
	body := string(found.Body)
	if !strings.Contains(body, "<h2>") || !strings.Contains(body, "AADSTS50011") {
		t.Errorf("the guide body looks unrendered: %.200q", body)
	}
	// The README links to it as a site page, not as a file in the repository.
	index := string(site[0].Body)
	if !strings.Contains(index, `href="guide-app-registration.html"`) {
		t.Errorf("the README does not link the guide page")
	}
}

func TestFirstHeading(t *testing.T) {
	tests := map[string]string{
		"## teams post\n\nPost a message.\n": "teams post",
		"# Title\n":                          "Title",
		"lorem\n\n### Deep\n":                "Deep",
		"no heading here\n":                  "",
		"#\n## after an empty one\n":         "after an empty one",
	}
	for markdown, want := range tests {
		if got := firstHeading(markdown); got != want {
			t.Errorf("firstHeading(%q) = %q, want %q", markdown, got, want)
		}
	}
}

func TestCommandLinkRewriterPointsAtSiblingPages(t *testing.T) {
	in := "### SEE ALSO\n\n* [teams alias](teams_alias.md)\t - Name a person\n* [teams](teams.md)\n"
	got := commandLinkRewriter(in)
	for _, want := range []string{"(teams_alias.html)", "(teams.html)"} {
		if !strings.Contains(got, want) {
			t.Errorf("rewritten = %q, want it to contain %q", got, want)
		}
	}
	// A link out of the site is left alone.
	extern := "see [the docs](../ci.md) and [Graph](https://learn.microsoft.com)\n"
	if got := commandLinkRewriter(extern); got != extern {
		t.Errorf("rewritten = %q, want external links unchanged", got)
	}
}

func TestDocLinkRewriter(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "docs", "commands"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "docs", "commands", "teams_post.md"), []byte("## teams post\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "a command page becomes a site page",
			in:   "see [teams post](docs/commands/teams_post.md)",
			want: "see [teams post](teams_post.html)",
		},
		{
			name: "a fragment stays on the page",
			in:   "see [Status](#status)",
			want: "see [Status](#status)",
		},
		{
			name: "the readme links to itself",
			in:   "back to [the top](README.md)",
			want: "back to [the top](index.html)",
		},
		{
			name: "a repository file goes to GitHub",
			in:   "see [PLAN.md](PLAN.md) and [docs](docs/ci.md)",
			want: "see [PLAN.md](https://github.com/floriscornel/teams-cli/blob/main/PLAN.md) and [docs](https://github.com/floriscornel/teams-cli/blob/main/docs/ci.md)",
		},
		{
			name: "an external link is left alone",
			in:   "[releases](https://github.com/floriscornel/teams-cli/releases)",
			want: "[releases](https://github.com/floriscornel/teams-cli/releases)",
		},
		{
			name: "an anchor on a repository file survives",
			in:   "see [the design](PLAN.md#phase-5-polish-and-v10)",
			want: "see [the design](https://github.com/floriscornel/teams-cli/blob/main/PLAN.md#phase-5-polish-and-v10)",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := docLinkRewriter(tc.in, root); got != tc.want {
				t.Errorf("docLinkRewriter(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestBuildSiteRendersEveryPage(t *testing.T) {
	// The real repository: README.md and docs/commands are committed, so the
	// build must work from a checkout with no extra setup.
	root := filepath.Join("..", "..", "..")
	if _, err := os.Stat(filepath.Join(root, "README.md")); err != nil {
		t.Skipf("no checkout to render: %v", err)
	}
	site, err := buildSite(root)
	if err != nil {
		t.Fatalf("buildSite: %v", err)
	}
	if len(site) < 10 {
		t.Fatalf("site has %d pages, want the README plus every command", len(site))
	}
	if site[0].File != "index.html" {
		t.Errorf("first page = %q, want index.html", site[0].File)
	}
	for _, p := range site {
		if p.Title == "" {
			t.Errorf("page %s has no title", p.File)
		}
		if !strings.Contains(string(p.Body), "<") {
			t.Errorf("page %s has no rendered HTML", p.File)
		}
		switch {
		case p.File == "index.html", strings.HasPrefix(p.File, "guide-"):
			if p.Section != "Guide" {
				t.Errorf("page %s is in section %q, want Guide", p.File, p.Section)
			}
		default:
			if p.Section != "Commands" {
				t.Errorf("page %s is in section %q, want Commands", p.File, p.Section)
			}
		}
	}
}
