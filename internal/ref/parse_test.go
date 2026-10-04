package ref

import (
	"errors"
	"strings"
	"testing"
)

// This file covers Parse: the classification half of PLAN.md:164-165's "Smart
// references". A Teams deep link is delegated to ParseURL, whose contract
// internal/ref/url_test.go owns and this file deliberately does not repeat; what
// is asserted here is the delegation itself plus the cases Parse decides on its
// own: "@name", an e-mail or UPN, a name path of one, two or three segments, a
// raw channel or chat id, a GUID and a bare word (internal/ref/parse.go).

// Documented Teams deep links (PLAN.md:157, PLAN.md:160-161;
// refs/msteams/msteams-platform/concepts/build-and-test/deep-link-teams.md).
const (
	parseDocChannelMsgURL = "https://teams.microsoft.com/l/message/19:3997a8734ee5432bb9cdedb7c432ae7d@thread.tacv2/1648741500652?tenantId=f4c2b8e1-7a3d-4e6f-9b12-8c5d0e3a1f47&groupId=3606f714-ec2e-41b3-9ad1-6afb331bd35d&parentMessageId=1648741500652&teamName=Product%20Launch&channelName=General&createdTime=1648741500652"
	parseDocChatMsgURL    = "https://teams.microsoft.com/l/message/19:253f5895-9a62-4362-8d38-43f0205c702c_f1b94dcf-0aa3-4989-bcdf-ef4a5ed00f86@unq.gbl.spaces/1563480968434?context=%7B%22contextType%22:%22chat%22%7D"
	parseDocChannelURL    = "https://teams.microsoft.com/l/channel/19%3A9be3de4e70874c71a608dee9ba803ed3%40thread.tacv2/My%20example%20channel?groupId=72602e12-78ac-474c-99d6-f619710353a9&tenantId=aaaabbbb-0000-cccc-1111-dddd2222eeee"

	// parseRawChannelID and parseRawChatID are the shapes LooksLikeChannelID and
	// LooksLikeChatID test. The table cases that parse a raw id spell their input
	// out, because a raw id carrying an "@" and a dot mis-parses today (see the
	// DEFECT notes there and BUGS-REPORTED.md).
	parseRawChannelID   = "19:9be3de4e70874c71a608dee9ba803ed3@thread.tacv2"
	parseRawChatID      = "19:253f5895-9a62-4362-8d38-43f0205c702c@thread.v2"
	parseCloudChannelID = "19:abc@thread.tacv2"
	parseGUID           = "11111111-2222-3333-4444-555555555555"
)

// TestParseTable is the classification table of Parse: an empty input is a usage
// error (internal/ref/parse.go:15-17), a Teams URL is delegated to ParseURL
// (parse.go:18-24, PLAN.md:164), "@name" and an e-mail are the person forms
// (PLAN.md:166), "a/b/c" is a name path (PLAN.md:165, parse.go:34-54), and a bare
// word stays unclassified because the resolver, not the parser, decides what it
// means (internal/ref/reference.go:21-32).
func TestParseTable(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		raw     string
		want    Ref
		wantErr string
		// wantUsageCode asserts the exit code of an error case. It is set only
		// where the implementation carries a *ref.Error; every error case still
		// asserts that an error arrives at all.
		wantUsageCode bool
	}{
		{
			name: "documented channel message link",
			raw:  parseDocChannelMsgURL,
			want: Ref{
				Kind: KindMessage, Raw: parseDocChannelMsgURL, Host: "teams.microsoft.com",
				TeamID: "3606f714-ec2e-41b3-9ad1-6afb331bd35d", TeamName: "Product Launch",
				ChannelID:   "19:3997a8734ee5432bb9cdedb7c432ae7d@thread.tacv2",
				ChannelName: "General", MessageID: "1648741500652", ParentMessageID: "1648741500652",
				TenantID: "f4c2b8e1-7a3d-4e6f-9b12-8c5d0e3a1f47",
			},
		},
		{
			name: "documented chat message link is discriminated by the query string",
			raw:  parseDocChatMsgURL,
			// PLAN.md:158 and refs/msteams/…/deep-link-teams.md: /l/message carries both
			// families and context={"contextType":"chat"} is the only discriminator.
			want: Ref{
				Kind: KindMessage, Raw: parseDocChatMsgURL, Host: "teams.microsoft.com",
				ChatID:    "19:253f5895-9a62-4362-8d38-43f0205c702c_f1b94dcf-0aa3-4989-bcdf-ef4a5ed00f86@unq.gbl.spaces",
				MessageID: "1563480968434", InChat: true,
			},
		},
		{
			name: "documented channel link with a percent-encoded channel id",
			raw:  parseDocChannelURL,
			want: Ref{
				Kind: KindChannel, Raw: parseDocChannelURL, Host: "teams.microsoft.com",
				TeamID:      "72602e12-78ac-474c-99d6-f619710353a9",
				ChannelID:   "19:9be3de4e70874c71a608dee9ba803ed3@thread.tacv2",
				ChannelName: "My example channel",
				TenantID:    "aaaabbbb-0000-cccc-1111-dddd2222eeee",
			},
		},
		{
			name: "teams.cloud.microsoft host is accepted defensively",
			raw:  "https://teams.cloud.microsoft/l/channel/" + parseCloudChannelID + "/General?groupId=t-cloud",
			// PLAN.md:164 accepts the newer host defensively: the mirror does not
			// document it, so url.go's host list is a deliberate extension. The fuzz
			// seed below covers it too.
			want: Ref{
				Kind: KindChannel, Raw: "https://teams.cloud.microsoft/l/channel/" + parseCloudChannelID + "/General?groupId=t-cloud",
				Host: "teams.cloud.microsoft", TeamID: "t-cloud",
				ChannelID: parseCloudChannelID, ChannelName: "General",
			},
		},
		{
			name: "a deep link family the CLI cannot open is a usage error",
			raw:  "https://teams.microsoft.com/l/app/00000000-0000-0000-0000-000000000000?source=post",
			// PLAN.md:164 lists /l/app, /l/entity, /l/task, /l/call and /l/meeting* as
			// the families to reject with a usage error.
			// The error is asserted by message only: internal/ref/url.go:366-369
			// returns a plain error, which carries no *ref.Error and therefore no
			// exit code, so this case pins the rejection itself.
			wantErr: "cannot open the Teams deep link family /l/app",
		},
		{
			name:    "meeting deep links are rejected too",
			raw:     "https://teams.microsoft.com/l/meeting-join/19:meeting_x@thread.v2?context=abc",
			wantErr: "cannot open the Teams deep link family /l/meeting",
		},
		{
			name: "@name is the person form",
			raw:  "@alice",
			want: Ref{Kind: KindUser, Raw: "@alice", User: "alice"},
		},
		{
			name: "an e-mail is the person form and resolves exactly",
			raw:  "alice@contoso.example",
			want: Ref{Kind: KindUser, Raw: "alice@contoso.example", User: "alice@contoso.example", IsEmail: true},
		},
		{
			name: "a UPN is an e-mail-shaped person form",
			raw:  "alice@contoso.com",
			want: Ref{Kind: KindUser, Raw: "alice@contoso.com", User: "alice@contoso.com", IsEmail: true},
		},
		{
			name: "a word with an @ and no dot is not an address",
			raw:  "alice@contoso",
			// internal/ref/parse.go:82-88: an address needs a dot after the "@".
			want: Ref{Kind: KindUnknown, Raw: "alice@contoso"},
		},
		{
			name: "a bare team name stays unclassified",
			raw:  "Engineering",
			// PLAN.md:169 accepts a bare name, but Parse cannot tell a team name from a
			// chat topic or a person name, so it stays KindUnknown and Team() resolves
			// it by name (internal/ref/parse.go:59-63,
			// internal/ref/resolve.go:105-106).
			want: Ref{Kind: KindUnknown, Raw: "Engineering"},
		},
		{
			name: "a two-segment name path is a channel path",
			raw:  "Engineering/General",
			want: Ref{
				Kind: KindChannel, Raw: "Engineering/General", Path: []string{"Engineering", "General"},
				TeamName: "Engineering", ChannelName: "General",
			},
		},
		{
			name: "a three-segment name path is a message path",
			raw:  "Engineering/General/1758011222333",
			want: Ref{
				Kind: KindMessage, Raw: "Engineering/General/1758011222333",
				Path:     []string{"Engineering", "General", "1758011222333"},
				TeamName: "Engineering", ChannelName: "General", MessageID: "1758011222333",
			},
		},
		{
			name: "a four-segment name path is rejected",
			raw:  "Engineering/General/Thread/1758011222333",
			// PLAN.md:165 documents exactly Team/Channel and Team/Channel/MessageId.
			wantErr:       "has 4 segments",
			wantUsageCode: true,
		},
		{
			name:          "an empty segment in a name path is rejected",
			raw:           "Engineering//General",
			wantErr:       "has an empty segment",
			wantUsageCode: true,
		},
		{
			name:          "a leading empty segment in a name path is rejected",
			raw:           "/General",
			wantErr:       "has an empty segment",
			wantUsageCode: true,
		},
		{
			name: "a raw channel id is classified as a channel",
			raw:  parseRawChannelID,
			// PLAN.md:169 accepts "a raw ID" and PLAN.md:164 prints a channel id in
			// exactly this shape, so the id wins over the e-mail form: the "@" of
			// 19:...@thread.tacv2 belongs to an id, not to an address
			// (internal/ref/parse.go:26-29, parse.go:82-90).
			want: Ref{Kind: KindChannel, Raw: parseRawChannelID, ChannelID: parseRawChannelID},
		},
		{
			name: "a raw chat id is classified as a chat",
			raw:  parseRawChatID,
			// The id refs/graph/api-reference/v1.0/api/chat-list-messages.md:79
			// documents.
			want: Ref{Kind: KindChat, Raw: parseRawChatID, ChatID: parseRawChatID},
		},
		{
			name: "a chat id with the unq.gbl.spaces suffix is a chat, not an address",
			raw:  "19:1273a016-201d-4f95-8083-1b7f99b3edeb_976f4b31-fd01-4e0b-9178-29cc40c14438@unq.gbl.spaces",
			// That suffix contains a dot and the id still is a chat, because the
			// 19: prefix decides (internal/ref/reference.go:101-107). The id is the
			// one refs/graph/concepts/teams-changenotifications-chat.md:223 shows.
			want: Ref{
				Kind:   KindChat,
				Raw:    "19:1273a016-201d-4f95-8083-1b7f99b3edeb_976f4b31-fd01-4e0b-9178-29cc40c14438@unq.gbl.spaces",
				ChatID: "19:1273a016-201d-4f95-8083-1b7f99b3edeb_976f4b31-fd01-4e0b-9178-29cc40c14438@unq.gbl.spaces",
			},
		},
		{
			name: "a raw chat id with no @ is classified as a chat",
			raw:  "19:2da4c29f6d7041eca70b638b43d45437",
			want: Ref{Kind: KindChat, Raw: "19:2da4c29f6d7041eca70b638b43d45437", ChatID: "19:2da4c29f6d7041eca70b638b43d45437"},
		},
		{
			name: "a GUID stays unclassified for the command to decide",
			raw:  parseGUID,
			// A team id is a GUID, but so is a user id: only the resolver knows which
			// the command meant (internal/ref/reference.go:21-32).
			want: Ref{Kind: KindUnknown, Raw: parseGUID},
		},
		{
			name: "an unknown bare word stays unclassified",
			raw:  "Release train",
			want: Ref{Kind: KindUnknown, Raw: "Release train"},
		},
		{
			name:          "an empty reference is a usage error",
			raw:           "   ",
			wantErr:       "empty reference",
			wantUsageCode: true,
		},
		{
			name:          "@ without a name is a usage error",
			raw:           "@",
			wantErr:       "@ needs a name",
			wantUsageCode: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := Parse(tc.raw)
			if tc.wantErr != "" {
				if err == nil {
					t.Fatalf("Parse(%q) = %+v, want an error containing %q", tc.raw, got, tc.wantErr)
				}
				if !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("Parse(%q) error = %q, want it to contain %q", tc.raw, err.Error(), tc.wantErr)
				}
				if tc.wantUsageCode {
					if code := exitCodeOf(t, err); code != 2 {
						t.Fatalf("Parse(%q) exit code = %d, want 2: a reference that cannot be interpreted is a usage error (internal/ref/errors.go:35-37)", tc.raw, code)
					}
				}
				return
			}
			if err != nil {
				t.Fatalf("Parse(%q) error = %v, want none", tc.raw, err)
			}
			if !refEqual(got, tc.want) {
				t.Fatalf("Parse(%q) =\n  %+v\nwant\n  %+v", tc.raw, got, tc.want)
			}
		})
	}
}

// TestParsePersonKey covers the cache key of a person reference: the typed text
// lower-cased and without the leading "@", so "@Yuki" and "yuki" share a key while
// "yuki@x.com" stays distinct, because the cache records which form resolved to
// whom (internal/ref/parse.go:90-96, PLAN.md:166).
func TestParsePersonKey(t *testing.T) {
	t.Parallel()

	cases := []struct{ raw, want string }{
		{"@Alice", "alice"},
		{"alice", "alice"},
		{"  ALICE  ", "alice"},
		{"alice@contoso.example", "alice@contoso.example"},
		{"@Alice@Contoso.Example", "alice@contoso.example"},
		{"@@alice", "@alice"}, // only the first "@" is the person marker
		{"", ""},
	}
	for _, tc := range cases {
		if got := PersonKey(tc.raw); got != tc.want {
			t.Errorf("PersonKey(%q) = %q, want %q", tc.raw, got, tc.want)
		}
	}
}

// TestParseIDShapeHelpers covers the two shape predicates the parser uses to tell
// a channel id from a chat id. Both start with "19:" and only the @thread.tacv2
// suffix separates the two families (internal/ref/reference.go:97-112;
// refs/msteams/msteams-platform/concepts/build-and-test/deep-link-teams.md).
func TestParseIDShapeHelpers(t *testing.T) {
	t.Parallel()

	cases := []struct {
		in      string
		chat    bool
		channel bool
		why     string
	}{
		{parseRawChannelID, false, true, "a channel id ends in @thread.tacv2"},
		{parseRawChatID, true, false, "a chat id ends in @thread.v2"},
		{"19:abc@thread.tacv2", false, true, "the channel suffix decides"},
		{"19:abc@THREAD.TACV2", false, true, "the suffix comparison is case-insensitive"},
		{"19:abc@THREAD.V2", true, false, "the chat family is the absence of the channel suffix"},
		{"19:", true, false, "the prefix alone is chat-shaped, not channel-shaped"},
		{"48:notes", false, false, "the self chat's documented id is not a 19: id"},
		{"abc", false, false, "no 19: prefix"},
		{"", false, false, "empty"},
	}
	for _, tc := range cases {
		if got := LooksLikeChatID(tc.in); got != tc.chat {
			t.Errorf("LooksLikeChatID(%q) = %v, want %v (%s)", tc.in, got, tc.chat, tc.why)
		}
		if got := LooksLikeChannelID(tc.in); got != tc.channel {
			t.Errorf("LooksLikeChannelID(%q) = %v, want %v (%s)", tc.in, got, tc.channel, tc.why)
		}
	}
}

// FuzzParse asserts the two properties Parse must always have: it never panics on
// any input (PLAN.md:164 requires tolerant parsing of pasted links, any parameter
// order and unknown parameters included) and every successful parse carries Raw,
// because Raw is both the text a caller echoes into an error and the entity cache
// key (internal/ref/reference.go:38, PLAN.md:170).
func FuzzParse(f *testing.F) {
	f.Add("")
	f.Add("   ")
	f.Add("@alice")
	f.Add("alice@contoso.example")
	f.Add("alice@contoso")
	f.Add("Engineering/General")
	f.Add("Engineering/General/1758011222333")
	f.Add("Engineering//General")
	f.Add("a/b/c/d")
	f.Add(parseRawChannelID)
	f.Add(parseRawChatID)
	f.Add("19:")
	f.Add(parseGUID)
	f.Add("https://teams.microsoft.com/l/app/x")
	f.Add(parseDocChannelMsgURL)
	f.Add(parseDocChatMsgURL)
	f.Add(parseDocChannelURL)
	f.Add("https://teams.cloud.microsoft/l/channel/" + parseCloudChannelID + "/General?groupId=t")
	f.Add("msteams://teams.microsoft.com/l/message/19:abc@thread.v2/1?context=%7B%22contextType%22:%22chat%22%7D")
	f.Add("%")
	f.Add("https://teams.microsoft.com/l/message/")
	f.Add("https://teams.microsoft.com/l/channel/")
	f.Add("//")

	f.Fuzz(func(t *testing.T, raw string) {
		parsed, err := Parse(raw)
		if err != nil {
			if parsed.Raw != "" {
				t.Fatalf("Parse(%q) returned a Ref with Raw %q and an error", raw, parsed.Raw)
			}
			return
		}
		if parsed.Raw == "" {
			t.Fatalf("Parse(%q) returned a Ref without Raw and no error; PLAN.md:164-169 needs the typed text for error messages and the entity cache key", raw)
		}
	})
}

// refEqual compares two parsed references field by field, so a parse test cannot
// pass by leaving a field at its zero value.
func refEqual(a, b Ref) bool {
	return a.Kind == b.Kind && a.Raw == b.Raw && a.TeamID == b.TeamID && a.TeamName == b.TeamName &&
		a.ChannelID == b.ChannelID && a.ChannelName == b.ChannelName && a.ChatID == b.ChatID &&
		a.ChatTopic == b.ChatTopic && a.MessageID == b.MessageID && a.ParentMessageID == b.ParentMessageID &&
		a.InChat == b.InChat && a.User == b.User && a.UserID == b.UserID && a.UserName == b.UserName &&
		a.UserMail == b.UserMail && a.IsEmail == b.IsEmail && a.FileID == b.FileID &&
		strings.Join(a.Path, "/") == strings.Join(b.Path, "/") && a.Host == b.Host && a.TenantID == b.TenantID
}

// exitCodeOf returns the exit code a reference error carries, which is the process
// exit code the CLI contract assigns (internal/ref/errors.go:19-32; AGENTS.md
// "Exit codes": 2 usage, 4 not found). It reads the exported behaviour only.
func exitCodeOf(t *testing.T, err error) int {
	t.Helper()
	var refErr *Error
	if !errors.As(err, &refErr) {
		t.Fatalf("error %v (%T) does not carry an exit code; want a *ref.Error", err, err)
	}
	return refErr.ExitCode()
}
