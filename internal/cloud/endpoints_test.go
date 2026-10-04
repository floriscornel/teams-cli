package cloud

import (
	"errors"
	"strings"
	"testing"
)

func TestLookupReturnsTheDocumentedClouds(t *testing.T) {
	for _, name := range Names() {
		e, ok := Lookup(name)
		if !ok {
			t.Fatalf("Lookup(%q) failed but Names() lists it", name)
		}
		if e.Name != name {
			t.Errorf("Name = %q, want %q", e.Name, name)
		}
		if e.AuthorityHost == "" || !strings.HasPrefix(e.AuthorityHost, "https://") {
			t.Errorf("%s authority = %q, want an https host", name, e.AuthorityHost)
		}
		if e.Documented == "" {
			t.Errorf("%s has no refs/ citation; every value needs one", name)
		}
	}
	if _, ok := Lookup("global"); !ok {
		t.Error("the global cloud is missing")
	}
	if _, ok := Lookup("usgov"); !ok {
		t.Error("the usgov cloud is missing")
	}
	if _, ok := Lookup(""); !ok {
		t.Error("an empty cloud name must mean global")
	}
	if _, ok := Lookup("moon"); ok {
		t.Error("Lookup accepted an unknown cloud")
	}
}

func TestGraphBaseIsDocumentedPerCloud(t *testing.T) {
	global := MustLookup("global")
	if global.Graph != "https://graph.microsoft.com/v1.0" {
		t.Errorf("global graph = %q", global.Graph)
	}
	if !global.DocumentedGraphBase() {
		t.Error("the global Graph base is documented and must say so")
	}
	usgov := MustLookup("usgov")
	if usgov.Graph != "https://graph.microsoft.us/v1.0" {
		t.Errorf("usgov graph = %q", usgov.Graph)
	}
	china := MustLookup("china")
	// The mirror documents the China authority but not the Graph service root,
	// so the profile has to supply it rather than us guessing a host.
	if china.Graph != "" || china.DocumentedGraphBase() {
		t.Errorf("china graph = %q; the mirror does not document it", china.Graph)
	}
	if !strings.Contains(china.Documented, "authentication-national-cloud.md:65") {
		t.Errorf("china citation = %q", china.Documented)
	}
	if !strings.Contains(china.AuthorityHost, "partner.microsoftonline.cn") {
		t.Errorf("china authority = %q", china.AuthorityHost)
	}
	if !errors.Is(ErrGraphBaseUndocumented, ErrGraphBaseUndocumented) {
		t.Error("ErrGraphBaseUndocumented is not comparable")
	}
}

func TestAuthorityAndURLHelpers(t *testing.T) {
	e := MustLookup("global")
	if got := e.Authority("colorkrew.com"); got != "https://login.microsoftonline.com/colorkrew.com" {
		t.Errorf("Authority = %q", got)
	}
	// The tenant segment is mandatory for MSAL, so an empty tenant falls back to
	// `common` rather than producing a URL MSAL rejects.
	if got := e.Authority(""); got != "https://login.microsoftonline.com/common" {
		t.Errorf("Authority(\"\") = %q", got)
	}
	if got := e.AuthorityHost; !strings.HasPrefix(got, "https://") {
		t.Errorf("AuthorityHost = %q", got)
	}
	if got := e.GraphURL("/me/chats"); got != "https://graph.microsoft.com/v1.0/me/chats" {
		t.Errorf("GraphURL = %q", got)
	}
	if got := e.GraphURL("me/chats"); got != "https://graph.microsoft.com/v1.0/me/chats" {
		t.Errorf("GraphURL without a leading slash = %q", got)
	}
	if got := e.KeyVaultURL("kv-teams-cli"); got != "https://kv-teams-cli.vault.azure.net/" {
		t.Errorf("KeyVaultURL = %q", got)
	}
	if got := MustLookup("usgov").KeyVaultURL("kv"); !strings.Contains(got, "usgovcloudapi.net") {
		t.Errorf("usgov KeyVaultURL = %q", got)
	}
}

func TestMustLookupPanicsOnUnknownCloud(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("MustLookup did not panic for an unknown cloud")
		}
	}()
	MustLookup("moon")
}

func TestNamesAreSorted(t *testing.T) {
	names := Names()
	for i := 1; i < len(names); i++ {
		if names[i-1] > names[i] {
			t.Fatalf("Names() is not sorted: %v", names)
		}
	}
}
