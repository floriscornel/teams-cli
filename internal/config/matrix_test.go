package config

import (
	"errors"
	"strings"
	"testing"
)

func TestScopesForResolvesPresetsAndLists(t *testing.T) {
	for _, name := range Scopes.Names() {
		got, err := Scopes.For(name)
		if err != nil {
			t.Fatalf("For(%q) = %v", name, err)
		}
		if len(got) == 0 {
			t.Errorf("preset %q is empty", name)
		}
		// A caller mutating the result must not change the preset.
		got[0] = "Mutated"
		if Scopes.Preset(name)[0] == "Mutated" {
			t.Errorf("preset %q hands out its own slice", name)
		}
	}
	// An empty spec means the full preset (mode = "full" with no scopes set).
	got, err := Scopes.For("")
	if err != nil || len(got) != len(Presets[ScopePresetFull]) {
		t.Fatalf("For(\"\") = (%v, %v), want the full preset", got, err)
	}
	got, err = Scopes.For("  full  ")
	if err != nil || len(got) != len(Presets[ScopePresetFull]) {
		t.Fatalf("For(\"  full  \") = (%v, %v)", got, err)
	}
	// Explicit lists are accepted, de-duplicated, and order is preserved.
	got, err = Scopes.For("User.Read,Chat.Read User.Read\nFiles.Read.All")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(got, " ") != "User.Read Chat.Read Files.Read.All" {
		t.Errorf("explicit list = %v", got)
	}
	if _, err := Scopes.For(","); err == nil {
		t.Error("For(\",\") accepted an empty list")
	}
	if _, err := Scopes.For("not a scope!"); err == nil {
		t.Error("For accepted a malformed scope name")
	}
	if _, err := Scopes.For("openid"); err == nil {
		t.Error("For accepted openid, which MSAL appends itself")
	}
	if _, err := Scopes.For("OFFLINE_ACCESS"); err == nil {
		t.Error("For accepted offline_access case-insensitively")
	}
}

func TestScopesPresetMetadata(t *testing.T) {
	if names := Scopes.Names(); strings.Join(names, ",") != "chats,read-only,full" {
		t.Errorf("Names() = %v, want least to most access", names)
	}
	if got := Scopes.Preset("nope"); got != nil {
		t.Errorf("Preset(nope) = %v, want nil", got)
	}
	// Full must be a superset of read-only: that is what makes it a preset tier.
	if missing := Scopes.Missing(Presets[ScopePresetFull], Presets[ScopePresetReadOnly]); len(missing) != 0 {
		t.Errorf("full is missing read-only scopes: %v", missing)
	}
	// The corrections from PLAN.md must be present.
	for _, want := range []string{"Files.Read.All", "People.Read", "User.ReadBasic.All"} {
		if !Scopes.Known(want) {
			t.Errorf("%s is not in any preset", want)
		}
	}
	// ChannelMessage.Edit is documented but authorises nothing we need.
	for _, preset := range Scopes.Names() {
		for _, scope := range Scopes.Preset(preset) {
			if scope == "ChannelMessage.Edit" {
				t.Errorf("preset %q still contains the inert ChannelMessage.Edit", preset)
			}
			if scope == "Chat.ManageDeletion.All" {
				t.Errorf("preset %q contains an incremental scope", preset)
			}
		}
	}
	if got := Scopes.Containing("Chat.ManageDeletion.All"); got != "" {
		t.Errorf("Containing = %q, want empty for an incremental scope", got)
	}
	if got := Scopes.Containing("chat.read"); got != ScopePresetChats {
		t.Errorf("Containing is not case-insensitive: %q", got)
	}
	if !Scopes.Known("Chat.ManageDeletion.All") {
		t.Error("the incremental scope is not reported as known")
	}
	if Scopes.Known("Nope.Read") {
		t.Error("Known accepted an unknown scope")
	}
	for _, scope := range DelegatedAdminConsentScopes {
		if !Scopes.RequiresAdminConsent(scope) {
			t.Errorf("%s should need admin consent", scope)
		}
	}
	if Scopes.RequiresAdminConsent("User.Read") {
		t.Error("User.Read does not need admin consent")
	}
	// The scopes PLAN.md explicitly says are not documented as delegated
	// admin-consent scopes.
	for _, scope := range []string{"Files.Read.All", "Files.ReadWrite.All"} {
		if Scopes.RequiresAdminConsent(scope) {
			t.Errorf("%s is not documented as needing admin consent", scope)
		}
	}
}

func TestParseGrantedScopesAndMissing(t *testing.T) {
	got := Scopes.ParseGrantedScopes("  Chat.Read   User.Read Chat.Read ")
	if strings.Join(got, " ") != "Chat.Read User.Read" {
		t.Errorf("ParseGrantedScopes = %v, want sorted and de-duplicated", got)
	}
	if got := Scopes.ParseGrantedScopes(""); len(got) != 0 {
		t.Errorf("ParseGrantedScopes(\"\") = %v", got)
	}
	missing := Scopes.Missing([]string{"user.read"}, []string{"User.Read", "Chat.Read"})
	if len(missing) != 1 || missing[0] != "Chat.Read" {
		t.Errorf("Missing = %v, want only Chat.Read (comparison must be case-insensitive)", missing)
	}
	if missing := Scopes.Missing(nil, nil); missing != nil {
		t.Errorf("Missing(nil, nil) = %v", missing)
	}
}

func TestFeatureMatrixIsWellFormed(t *testing.T) {
	features := Features()
	if len(features) < 30 {
		t.Fatalf("the matrix has only %d rows", len(features))
	}
	seen := map[string]bool{}
	lastPhase := 0
	for _, f := range features {
		if seen[f.ID] {
			t.Errorf("duplicate feature id %q", f.ID)
		}
		seen[f.ID] = true
		if f.Command == "" || len(f.Scopes) == 0 {
			t.Errorf("feature %q is incomplete: %+v", f.ID, f)
		}
		if !strings.HasPrefix(f.Source, "refs/") && !strings.HasPrefix(f.Source, "docs/") {
			t.Errorf("feature %q has no refs/ citation: %q", f.ID, f.Source)
		}
		if f.Phase < 2 {
			t.Errorf("feature %q has phase %d", f.ID, f.Phase)
		}
		if f.Phase < lastPhase {
			t.Errorf("features are not ordered by phase: %q after phase %d", f.ID, lastPhase)
		}
		lastPhase = f.Phase
		for _, scope := range f.Scopes {
			if !Scopes.Known(scope) {
				t.Errorf("feature %q needs %q, which no preset, incremental set or alternative table lists", f.ID, scope)
			}
		}
		if f.Preset == "" && !f.AdminConsent {
			t.Errorf("feature %q has neither a preset nor an admin-consent flag", f.ID)
		}
		// A row that names a preset must have at least one alternative the preset
		// actually contains, otherwise the "smallest preset that has it" hint is
		// wrong and a user would enable a preset that does not help.
		if f.Preset != "" {
			preset := Presets[f.Preset]
			found := false
			for _, scope := range f.Scopes {
				for _, inPreset := range preset {
					if strings.EqualFold(scope, inPreset) {
						found = true
					}
				}
			}
			if !found {
				t.Errorf("feature %q names preset %q, which contains none of %v", f.ID, f.Preset, f.Scopes)
			}
		}
	}
	// The two rows that encode PLAN.md's corrections.
	edit, ok := FeatureByID("edit-channel")
	if !ok || edit.Preset != ScopePresetFull || !edit.AdminConsent {
		t.Errorf("edit-channel = %+v, want the full preset with admin consent", edit)
	}
	deleteChat, ok := FeatureByID("chat-delete")
	if !ok || deleteChat.Preset != "" {
		t.Errorf("chat-delete = %+v, want no preset (incremental consent)", deleteChat)
	}
	if _, ok := FeatureByID("nope"); ok {
		t.Error("FeatureByID found a feature that does not exist")
	}
}

func TestEvaluateReportsGrantedAndMissing(t *testing.T) {
	statuses := Evaluate([]string{"User.Read", "Chat.Read"})
	byID := map[string]FeatureStatus{}
	for _, st := range statuses {
		byID[st.ID] = st
	}
	if !byID["whoami"].Granted {
		t.Error("whoami needs User.Read, which is granted")
	}
	if byID["channel-read"].Granted {
		t.Error("channel-read needs ChannelMessage.Read.All, which is not granted")
	}
	if missing := byID["channel-read"].Missing; len(missing) != 1 || missing[0] != "ChannelMessage.Read.All" {
		t.Errorf("channel-read missing = %v", missing)
	}
	// An any-of row is granted when one alternative is present.
	if !byID["chat-list"].Granted {
		t.Error("chat-list accepts Chat.Read, which is granted")
	}
	// With nothing granted, everything is missing but nothing panics.
	for _, st := range Evaluate(nil) {
		if st.Granted {
			t.Errorf("%s reported granted with no scopes", st.ID)
		}
	}
}

func TestScopeHintAndMissingScopeError(t *testing.T) {
	if got := ScopeHint("Chat.ReadWrite"); !strings.Contains(got, ScopePresetChats) {
		t.Errorf("ScopeHint(Chat.ReadWrite) = %q", got)
	}
	if got := ScopeHint("ChannelMessage.ReadWrite"); !strings.Contains(got, "admin") {
		t.Errorf("ScopeHint(ChannelMessage.ReadWrite) = %q, want the admin path", got)
	}
	if got := ScopeHint("Chat.ManageDeletion.All"); !strings.Contains(got, "chat delete") {
		t.Errorf("ScopeHint(Chat.ManageDeletion.All) = %q, want the incremental path", got)
	}
	if got := ScopeHint("Something.New"); !strings.Contains(got, "explicitly") {
		t.Errorf("ScopeHint(unknown) = %q", got)
	}

	err := NewMissingScopeError("teams channel read", []string{"ChannelMessage.Read.All"})
	if err == nil || !strings.Contains(err.Error(), "teams channel read needs ChannelMessage.Read.All") {
		t.Fatalf("NewMissingScopeError = %v", err)
	}
	if err.Hint == "" || err.Scope != "ChannelMessage.Read.All" {
		t.Errorf("error = %+v", err)
	}
	multi := NewMissingScopeError("teams whoami", []string{"User.Read", "User.ReadBasic.All"})
	if !strings.Contains(multi.Error(), "needs one of") {
		t.Errorf("a multi-scope error reads badly: %v", multi)
	}
	if NewMissingScopeError("cmd", nil) != nil {
		t.Error("NewMissingScopeError with no scopes should be nil")
	}
}

func TestEnsureScope(t *testing.T) {
	if err := EnsureScope("cmd", []string{"User.Read"}, nil); err != nil {
		t.Errorf("no required scopes = %v", err)
	}
	if err := EnsureScope("cmd", []string{"User.Read"}, []string{"User.Read", "Chat.Read"}); err != nil {
		t.Errorf("one satisfied alternative should pass: %v", err)
	}
	err := EnsureScope("teams whoami", []string{"Chat.Read"}, []string{"User.Read", "User.ReadBasic.All"})
	var missing *MissingScope
	if !errors.As(err, &missing) {
		t.Fatalf("EnsureScope = %v, want a *MissingScope", err)
	}
	if missing.Scope != "User.Read" {
		t.Errorf("Scope = %q", missing.Scope)
	}
}
