package cli

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/floriscornel/teams-cli/internal/graph"
	"github.com/floriscornel/teams-cli/internal/store"
)

// Unit tests for the Phase 3 read helpers: the flag parsers, the message
// renderer's building blocks and the small filters the commands share. The
// end-to-end behaviour lives in testdata/script/*.txtar; these tests pin the
// pieces a script cannot reach.

func TestParseTimeFlag(t *testing.T) {
	now := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name  string
		value string
		want  time.Time
	}{
		{"empty", "", time.Time{}},
		{"a full timestamp", "2026-10-01T08:30:00Z", time.Date(2026, 10, 1, 8, 30, 0, 0, time.UTC)},
		{"a date", "2026-10-01", time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)},
		{"hours", "24h", now.Add(-24 * time.Hour)},
		{"days", "7d", now.Add(-7 * 24 * time.Hour)},
		{"weeks", "2w", now.Add(-14 * 24 * time.Hour)},
		{"minutes", "30m", now.Add(-30 * time.Minute)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseTimeFlag("since", tc.value, now)
			if err != nil {
				t.Fatalf("parseTimeFlag(%q): %v", tc.value, err)
			}
			if !got.Equal(tc.want) {
				t.Errorf("parseTimeFlag(%q) = %s, want %s", tc.value, got, tc.want)
			}
		})
	}
}

func TestParseTimeFlagRejectsNonsense(t *testing.T) {
	_, err := parseTimeFlag("since", "yesterday", time.Now())
	if err == nil {
		t.Fatal("want a usage error for an unusable value")
	}
	if !strings.Contains(err.Error(), "--since") {
		t.Errorf("error %q does not name the flag", err)
	}
	if _, err := parseWindowDuration("h"); err == nil {
		t.Error("parseWindowDuration(h) = nil, want an error")
	}
}

func TestWindowFlagsWindow(t *testing.T) {
	now := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	since, until, err := windowFlags{since: "24h", until: "2026-10-04T00:00:00Z"}.window(now)
	if err != nil {
		t.Fatalf("window: %v", err)
	}
	if want := now.Add(-24 * time.Hour); !since.Equal(want) {
		t.Errorf("since = %s, want %s", since, want)
	}
	if until.IsZero() {
		t.Error("until is zero")
	}
	if _, _, err := (windowFlags{since: "1h", until: "2h"}).window(now); err == nil {
		t.Error("an inverted window was accepted")
	}
}

func TestListFlagsLimitOf(t *testing.T) {
	if got := (listFlags{}).limitOf(20); got != 20 {
		t.Errorf("default = %d, want 20", got)
	}
	if got := (listFlags{limit: 5}).limitOf(20); got != 5 {
		t.Errorf("limit = %d, want 5", got)
	}
	if got := (listFlags{limit: 5, all: true}).limitOf(20); got != 0 {
		t.Errorf("--all = %d, want 0 (every page)", got)
	}
}

func TestFormatTimeAndFirstNonEmpty(t *testing.T) {
	if got := formatTime(time.Time{}); got != "" {
		t.Errorf("formatTime(zero) = %q, want empty", got)
	}
	want := "2026-10-04T12:00:00Z"
	if got := formatTime(time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)); got != want {
		t.Errorf("formatTime = %q, want %q", got, want)
	}
	if got := firstNonEmptyString("", "  ", "b", "c"); got != "b" {
		t.Errorf("firstNonEmptyString = %q, want b", got)
	}
}

func TestHumanSizeAndItemKind(t *testing.T) {
	if got := humanSize(0); got != "" {
		t.Errorf("humanSize(0) = %q, want empty", got)
	}
	if got := humanSize(2048); got != "2.0 KiB" {
		t.Errorf("humanSize(2048) = %q, want 2.0 KiB", got)
	}
	if got := itemKind(graph.DriveItem{Folder: &graph.FolderFacet{}}); got != "folder" {
		t.Errorf("itemKind(folder) = %q", got)
	}
	if got := itemKind(graph.DriveItem{File: &graph.FileFacet{MimeType: "application/pdf"}}); got != "pdf" {
		t.Errorf("itemKind(pdf) = %q", got)
	}
	if got := itemKind(graph.DriveItem{}); got != "file" {
		t.Errorf("itemKind(unknown) = %q", got)
	}
}

func TestMessageAuthorAndLines(t *testing.T) {
	msg := graph.Message{
		From: &graph.IdentitySet{User: &graph.Identity{ID: "u1", DisplayName: "Alice"}},
		Attachments: []graph.Attachment{
			{Name: "notes.md"},
			{ID: "att-2"},
			{},
		},
		Reactions: []graph.Reaction{
			{ReactionType: "👍"}, {ReactionType: "👍"}, {ReactionType: "✅"},
		},
	}
	if got := messageAuthor(msg); got != "Alice" {
		t.Errorf("messageAuthor = %q, want Alice", got)
	}
	if got := attachmentLine(msg.Attachments); got != "attachment: notes.md, att-2, unnamed" {
		t.Errorf("attachmentLine = %q", got)
	}
	if got := reactionLine(msg.Reactions); got != "reactions: 👍 2 · ✅" {
		t.Errorf("reactionLine = %q", got)
	}
	if got := attachmentLine(nil); got != "" {
		t.Errorf("attachmentLine(nil) = %q", got)
	}
	if got := reactionLine(nil); got != "" {
		t.Errorf("reactionLine(nil) = %q", got)
	}

	unknown := graph.Message{}
	if got := messageAuthor(unknown); got != "unknown sender" {
		t.Errorf("messageAuthor(no sender) = %q", got)
	}
	system := graph.Message{MessageType: "systemEventMessage"}
	if got := messageAuthor(system); got != "Teams" {
		t.Errorf("messageAuthor(system event) = %q", got)
	}
}

func TestMentionNamesAndOrdering(t *testing.T) {
	names := mentionNames([]graph.Mention{
		{ID: 1, Mentioned: &graph.MentionedIdentitySet{User: &graph.TeamworkUserIdentity{DisplayName: "Yuki"}}},
		{ID: 2, MentionText: "Bob"},
	})
	if names[1] != "Yuki" || names[2] != "Bob" {
		t.Errorf("mentionNames = %v", names)
	}
	if mentionNames(nil) != nil {
		t.Error("mentionNames(nil) should stay nil")
	}

	msgs := []graph.Message{
		{ID: "b", CreatedDateTime: time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC)},
		{ID: "a", CreatedDateTime: time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)},
		{ID: "c", CreatedDateTime: time.Date(2026, 10, 3, 0, 0, 0, 0, time.UTC)},
	}
	sortMessagesChronological(msgs)
	if got := msgs[0].ID + msgs[1].ID + msgs[2].ID; got != "abc" {
		t.Errorf("sortMessagesChronological = %s, want abc", got)
	}
}

func TestChatHelpers(t *testing.T) {
	direct := graph.Chat{
		ID:       "19:bob@thread.v2",
		ChatType: "oneOnOne",
		Members: []graph.ConversationMember{
			{UserID: "me", DisplayName: "Alice"},
			{UserID: "bob", DisplayName: "Bob"},
		},
		LastMessagePreview: &graph.Message{CreatedDateTime: time.Date(2026, 10, 4, 10, 0, 0, 0, time.UTC)},
		Viewpoint:          &graph.ChatViewpoint{},
	}
	if got := chatTitle(direct); got != "Alice, Bob" {
		t.Errorf("chatTitle(1:1) = %q", got)
	}
	if got := unreadMark(direct); got != "yes" {
		t.Errorf("unreadMark(never read) = %q, want yes", got)
	}
	read := time.Date(2026, 10, 4, 11, 0, 0, 0, time.UTC)
	direct.Viewpoint.LastMessageReadDateTime = &read
	if got := unreadMark(direct); got != "no" {
		t.Errorf("unreadMark(read) = %q, want no", got)
	}
	if got := lastActivity(direct); got != "2026-10-04 10:00" {
		t.Errorf("lastActivity = %q", got)
	}
	if got := memberName(graph.ConversationMember{Email: "x@y"}); got != "x@y" {
		t.Errorf("memberName(email) = %q", got)
	}
	if got := memberName(graph.ConversationMember{UserID: "u"}); got != "u" {
		t.Errorf("memberName(id) = %q", got)
	}

	topic := "Release train"
	group := graph.Chat{
		ID: "19:group@thread.v2", ChatType: "group", Topic: &topic,
		Members: []graph.ConversationMember{{UserID: "me"}, {UserID: "bob"}, {UserID: "yuki"}},
	}
	chats := []graph.Chat{direct, group}
	if got := len(filterChatsWith(chats, "bob")); got != 1 {
		t.Errorf("filterChatsWith kept %d chats, want only the 1:1 one", got)
	}
	if got := len(filterChatsByTopic(chats, "train")); got != 1 {
		t.Errorf("filterChatsByTopic kept %d chats", got)
	}
	if got := len(filterUnreadChats(chats)); got != 0 {
		t.Errorf("filterUnreadChats kept %d chats, want none once the 1:1 is read", got)
	}
	names := chatTitle(graph.Chat{ID: "19:x@thread.v2", Members: []graph.ConversationMember{
		{DisplayName: "A"}, {DisplayName: "B"}, {DisplayName: "C"}, {DisplayName: "D"}, {DisplayName: "E"},
	}})
	if !strings.Contains(names, "+2") {
		t.Errorf("chatTitle with five members = %q, want a +2 summary", names)
	}
	if got := chatTitle(graph.Chat{ID: "19:empty@thread.v2"}); got != "19:empty@thread.v2" {
		t.Errorf("chatTitle without members = %q", got)
	}
}

func TestFilterMessagesBySender(t *testing.T) {
	msgs := []graph.Message{
		{ID: "1", From: &graph.IdentitySet{User: &graph.Identity{ID: "bob"}}},
		{ID: "2", From: &graph.IdentitySet{User: &graph.Identity{ID: "alice"}}},
	}
	got := filterMessagesBySender(msgs, "bob")
	if len(got) != 1 || got[0].ID != "1" {
		t.Errorf("filterMessagesBySender = %v", got)
	}
}

func TestSearchHelpers(t *testing.T) {
	channelHit := graph.SearchHit{Resource: graph.SearchResource{
		ChannelIdentity: &graph.ChannelIdentity{TeamID: "t", ChannelID: "c"},
	}}
	chatHit := graph.SearchHit{Resource: graph.SearchResource{ChatID: "19:x@thread.v2"}}
	if got := hitContainer(channelHit.Resource); got != "channel c" {
		t.Errorf("hitContainer(channel) = %q", got)
	}
	if got := hitContainer(chatHit.Resource); got != "chat 19:x@thread.v2" {
		t.Errorf("hitContainer(chat) = %q", got)
	}
	if got := hitSummary(graph.SearchHit{Summary: "  many   spaces "}); got != "many spaces" {
		t.Errorf("hitSummary(summary) = %q", got)
	}
	if got := hitSummary(graph.SearchHit{Resource: graph.SearchResource{Subject: "subj"}}); got != "subj" {
		t.Errorf("hitSummary(subject) = %q", got)
	}
	if got := hitSummary(graph.SearchHit{Resource: graph.SearchResource{ID: "m1"}}); got != "m1" {
		t.Errorf("hitSummary(id) = %q", got)
	}
	if got := pageFrom(1, 25); got != 0 {
		t.Errorf("pageFrom(1) = %d, want 0", got)
	}
	if got := pageFrom(3, 25); got != 50 {
		t.Errorf("pageFrom(3) = %d, want 50", got)
	}

	scope := &graph.SearchResource{ChannelIdentity: &graph.ChannelIdentity{TeamID: "t", ChannelID: "c"}}
	kept := filterHitsByScope([]graph.SearchHit{channelHit, chatHit}, scope)
	if len(kept) != 1 || kept[0].Resource.ChannelIdentity == nil {
		t.Errorf("filterHitsByScope(channel) = %v", kept)
	}
	chatScope := &graph.SearchResource{ChatID: "19:x@thread.v2"}
	kept = filterHitsByScope([]graph.SearchHit{channelHit, chatHit}, chatScope)
	if len(kept) != 1 || kept[0].Resource.ChatID == "" {
		t.Errorf("filterHitsByScope(chat) = %v", kept)
	}

	since := time.Date(2026, 10, 4, 10, 0, 0, 0, time.UTC)
	hits := []graph.SearchHit{
		{Resource: graph.SearchResource{CreatedDateTime: time.Date(2026, 10, 4, 9, 0, 0, 0, time.UTC)}},
		{Resource: graph.SearchResource{CreatedDateTime: time.Date(2026, 10, 4, 11, 0, 0, 0, time.UTC)}},
		{Resource: graph.SearchResource{}},
	}
	if got := len(filterHitsSince(hits, since)); got != 2 {
		t.Errorf("filterHitsSince kept %d hits, want the newer one and the undated one", got)
	}
	if got := len(filterHitsSince(hits, time.Time{})); got != 3 {
		t.Errorf("filterHitsSince with no window kept %d hits, want all", got)
	}
}

func TestDownloadHelpers(t *testing.T) {
	if got := sanitizeFileName("a/b\\c"); got != "a_b_c" {
		t.Errorf("sanitizeFileName = %q", got)
	}
	if got := sanitizeFileName(".."); got != "download" {
		t.Errorf("sanitizeFileName(..) = %q", got)
	}
	if got := sanitizeFileName("a\tb"); got != "a-b" {
		t.Errorf("sanitizeFileName(control) = %q", got)
	}
	cases := map[string]string{
		"image/png":       ".png",
		"image/jpeg":      ".jpg",
		"application/pdf": ".pdf",
		"text/markdown":   ".md",
		"application/xml": ".bin",
		"image/png; a=b":  ".png",
	}
	for ct, want := range cases {
		if got := extensionFor(ct); got != want {
			t.Errorf("extensionFor(%q) = %q, want %q", ct, got, want)
		}
	}

	items := []graph.DriveItem{
		{ID: "i1", Name: "other.txt"},
		{ID: "i2", Name: "notes.md", WebURL: "https://x/notes.md"},
	}
	if item, ok := matchDriveItem(items, graph.Attachment{Name: "notes.md"}); !ok || item.ID != "i2" {
		t.Errorf("matchDriveItem by name = %v, %v", item, ok)
	}
	if item, ok := matchDriveItem(items, graph.Attachment{ContentURL: "https://x/notes.md"}); !ok || item.ID != "i2" {
		t.Errorf("matchDriveItem by url = %v, %v", item, ok)
	}
	if _, ok := matchDriveItem(items, graph.Attachment{Name: "missing"}); ok {
		t.Error("matchDriveItem found an item that is not there")
	}
}

func TestChooseReadsANumber(t *testing.T) {
	var out strings.Builder
	app := New(strings.NewReader("2\n"), &out, &out)
	index, err := app.Choose("which one?", []string{"a", "b"})
	if err != nil {
		t.Fatalf("Choose: %v", err)
	}
	if index != 1 {
		t.Errorf("Choose = %d, want 1 (the second option)", index)
	}
	if !strings.Contains(out.String(), "which one?") || !strings.Contains(out.String(), "b") {
		t.Errorf("the prompt was not shown: %q", out.String())
	}

	bad := New(strings.NewReader("9\n"), &out, &out)
	if _, err := bad.Choose("which one?", []string{"a"}); err == nil {
		t.Error("an out-of-range answer was accepted")
	}
}

func TestSaveEntityCacheWritesOnlyWhenDirty(t *testing.T) {
	h := newHarness(t)
	paths, err := h.app.Paths()
	if err != nil {
		t.Fatalf("Paths: %v", err)
	}
	cache, err := store.LoadEntities(paths.EntityCacheFile())
	if err != nil {
		t.Fatalf("LoadEntities: %v", err)
	}
	cache.SetClock(h.app.Clock.Now)
	h.app.entityCache = cache
	h.app.saveEntityCache()
	if _, err := os.Stat(paths.EntityCacheFile()); err == nil {
		t.Error("a clean cache was written to disk")
	}
	cache.PutTeam("Engineering", "team-eng")
	h.app.saveEntityCache()
	if _, err := os.Stat(paths.EntityCacheFile()); err != nil {
		t.Errorf("a dirty cache was not written: %v", err)
	}
	reloaded, err := store.LoadEntities(paths.EntityCacheFile())
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if id, ok := reloaded.Team("engineering"); !ok || id != "team-eng" {
		t.Errorf("reloaded cache lost the team: %q, %v", id, ok)
	}
}
