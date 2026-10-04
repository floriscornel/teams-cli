package ref

import (
	"net/url"
	"reflect"
	"testing"
)

// Every URL below is copied verbatim from
// refs/msteams/msteams-platform/concepts/build-and-test/deep-link-teams.md; the
// "verbatim" cases keep the documented percent-encoding (both spellings of the
// channel id occur in the page). The remaining cases cover the tolerances
// refs/INDEX.md section 5 and PLAN.md:164 require: any parameter order, unknown
// parameters, an extra path segment, a trailing slash, and the second protocol
// handler (refs/msteams/msteams-platform/concepts/build-and-test/deep-links.md).
func TestParseURLDeepLinks(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		raw  string
		want Ref
		ok   bool
	}{
		{
			name: "documented channel message example",
			raw:  "https://teams.microsoft.com/l/message/19:3997a8734ee5432bb9cdedb7c432ae7d@thread.tacv2/1648741500652?tenantId=f4c2b8e1-7a3d-4e6f-9b12-8c5d0e3a1f47&groupId=3606f714-ec2e-41b3-9ad1-6afb331bd35d&parentMessageId=1648741500652&teamName=Product%20Launch&channelName=General&createdTime=1648741500652",
			want: Ref{
				Kind:            KindMessage,
				Raw:             "https://teams.microsoft.com/l/message/19:3997a8734ee5432bb9cdedb7c432ae7d@thread.tacv2/1648741500652?tenantId=f4c2b8e1-7a3d-4e6f-9b12-8c5d0e3a1f47&groupId=3606f714-ec2e-41b3-9ad1-6afb331bd35d&parentMessageId=1648741500652&teamName=Product%20Launch&channelName=General&createdTime=1648741500652",
				TeamID:          "3606f714-ec2e-41b3-9ad1-6afb331bd35d",
				TeamName:        "Product Launch",
				ChannelID:       "19:3997a8734ee5432bb9cdedb7c432ae7d@thread.tacv2",
				ChannelName:     "General",
				MessageID:       "1648741500652",
				ParentMessageID: "1648741500652",
				Host:            "teams.microsoft.com",
				TenantID:        "f4c2b8e1-7a3d-4e6f-9b12-8c5d0e3a1f47",
			},
			ok: true,
		},
		{
			name: "documented chat message example keeps the percent-encoded context",
			raw:  "https://teams.microsoft.com/l/message/19:253f5895-9a62-4362-8d38-43f0205c702c_f1b94dcf-0aa3-4989-bcdf-ef4a5ed00f86@unq.gbl.spaces/1563480968434?context=%7B%22contextType%22:%22chat%22%7D",
			want: Ref{
				Kind:      KindMessage,
				Raw:       "https://teams.microsoft.com/l/message/19:253f5895-9a62-4362-8d38-43f0205c702c_f1b94dcf-0aa3-4989-bcdf-ef4a5ed00f86@unq.gbl.spaces/1563480968434?context=%7B%22contextType%22:%22chat%22%7D",
				ChatID:    "19:253f5895-9a62-4362-8d38-43f0205c702c_f1b94dcf-0aa3-4989-bcdf-ef4a5ed00f86@unq.gbl.spaces",
				MessageID: "1563480968434",
				InChat:    true,
				Host:      "teams.microsoft.com",
			},
			ok: true,
		},
		{
			name: "chat message discriminator with an unencoded context",
			raw:  unencodedChatMessageURL(),
			want: Ref{
				Kind:      KindMessage,
				Raw:       unencodedChatMessageURL(),
				ChatID:    "19:253f5895-9a62-4362-8d38-43f0205c702c_f1b94dcf-0aa3-4989-bcdf-ef4a5ed00f86@unq.gbl.spaces",
				MessageID: "1563480968434",
				InChat:    true,
				Host:      "teams.microsoft.com",
			},
			ok: true,
		},
		{
			name: "channel message without groupId parses with an empty team id",
			raw:  "https://teams.microsoft.com/l/message/19:3997a8734ee5432bb9cdedb7c432ae7d@thread.tacv2/1648741500652?channelName=General",
			want: Ref{
				Kind:            KindMessage,
				Raw:             "https://teams.microsoft.com/l/message/19:3997a8734ee5432bb9cdedb7c432ae7d@thread.tacv2/1648741500652?channelName=General",
				ChannelID:       "19:3997a8734ee5432bb9cdedb7c432ae7d@thread.tacv2",
				ChannelName:     "General",
				MessageID:       "1648741500652",
				ParentMessageID: "1648741500652",
				Host:            "teams.microsoft.com",
			},
			ok: true,
		},
		{
			name: "message path with a percent-encoded channel id",
			raw:  "https://teams.microsoft.com/l/message/19%3A3997a8734ee5432bb9cdedb7c432ae7d%40thread.tacv2/1648741500652?groupId=3606f714-ec2e-41b3-9ad1-6afb331bd35d&tenantId=f4c2b8e1-7a3d-4e6f-9b12-8c5d0e3a1f47&parentMessageId=1648741500652&teamName=Product%20Launch&channelName=General&createdTime=1648741500652",
			want: Ref{
				Kind:            KindMessage,
				Raw:             "https://teams.microsoft.com/l/message/19%3A3997a8734ee5432bb9cdedb7c432ae7d%40thread.tacv2/1648741500652?groupId=3606f714-ec2e-41b3-9ad1-6afb331bd35d&tenantId=f4c2b8e1-7a3d-4e6f-9b12-8c5d0e3a1f47&parentMessageId=1648741500652&teamName=Product%20Launch&channelName=General&createdTime=1648741500652",
				TeamID:          "3606f714-ec2e-41b3-9ad1-6afb331bd35d",
				TeamName:        "Product Launch",
				ChannelID:       "19:3997a8734ee5432bb9cdedb7c432ae7d@thread.tacv2",
				ChannelName:     "General",
				MessageID:       "1648741500652",
				ParentMessageID: "1648741500652",
				Host:            "teams.microsoft.com",
				TenantID:        "f4c2b8e1-7a3d-4e6f-9b12-8c5d0e3a1f47",
			},
			ok: true,
		},
		{
			name: "documented standard channel example",
			raw:  "https://teams.microsoft.com/l/channel/19%3A9be3de4e70874c71a608dee9ba803ed3%40thread.tacv2/My%20example%20channel?groupId=72602e12-78ac-474c-99d6-f619710353a9&tenantId=aaaabbbb-0000-cccc-1111-dddd2222eeee",
			want: Ref{
				Kind:        KindChannel,
				Raw:         "https://teams.microsoft.com/l/channel/19%3A9be3de4e70874c71a608dee9ba803ed3%40thread.tacv2/My%20example%20channel?groupId=72602e12-78ac-474c-99d6-f619710353a9&tenantId=aaaabbbb-0000-cccc-1111-dddd2222eeee",
				TeamID:      "72602e12-78ac-474c-99d6-f619710353a9",
				ChannelID:   "19:9be3de4e70874c71a608dee9ba803ed3@thread.tacv2",
				ChannelName: "My example channel",
				Host:        "teams.microsoft.com",
				TenantID:    "aaaabbbb-0000-cccc-1111-dddd2222eeee",
			},
			ok: true,
		},
		{
			name: "documented private channel example tolerates ngc=true",
			raw:  "https://teams.microsoft.com/l/channel/19%3A9be3de4e70874c71a608dee9ba803ed3%40thread.tacv2/My%20example%20channel?groupId=72602e12-78ac-474c-99d6-f619710353a9&tenantId=aaaabbbb-0000-cccc-1111-dddd2222eeee&ngc=true",
			want: Ref{
				Kind:        KindChannel,
				Raw:         "https://teams.microsoft.com/l/channel/19%3A9be3de4e70874c71a608dee9ba803ed3%40thread.tacv2/My%20example%20channel?groupId=72602e12-78ac-474c-99d6-f619710353a9&tenantId=aaaabbbb-0000-cccc-1111-dddd2222eeee&ngc=true",
				TeamID:      "72602e12-78ac-474c-99d6-f619710353a9",
				ChannelID:   "19:9be3de4e70874c71a608dee9ba803ed3@thread.tacv2",
				ChannelName: "My example channel",
				Host:        "teams.microsoft.com",
				TenantID:    "aaaabbbb-0000-cccc-1111-dddd2222eeee",
			},
			ok: true,
		},
		{
			name: "documented shared channel example tolerates ngc and allowXTenantAccess",
			raw:  "https://teams.microsoft.com/l/channel/19%3A9be3de4e70874c71a608dee9ba803ed3%40thread.tacv2/My%20example%20channel?groupId=72602e12-78ac-474c-99d6-f619710353a9&tenantId=aaaabbbb-0000-cccc-1111-dddd2222eeee&ngc=true&allowXTenantAccess=true",
			want: Ref{
				Kind:        KindChannel,
				Raw:         "https://teams.microsoft.com/l/channel/19%3A9be3de4e70874c71a608dee9ba803ed3%40thread.tacv2/My%20example%20channel?groupId=72602e12-78ac-474c-99d6-f619710353a9&tenantId=aaaabbbb-0000-cccc-1111-dddd2222eeee&ngc=true&allowXTenantAccess=true",
				TeamID:      "72602e12-78ac-474c-99d6-f619710353a9",
				ChannelID:   "19:9be3de4e70874c71a608dee9ba803ed3@thread.tacv2",
				ChannelName: "My example channel",
				Host:        "teams.microsoft.com",
				TenantID:    "aaaabbbb-0000-cccc-1111-dddd2222eeee",
			},
			ok: true,
		},
		{
			name: "channel form with a raw channel id and a trailing slash",
			raw:  "https://teams.microsoft.com/l/channel/19:9be3de4e70874c71a608dee9ba803ed3@thread.tacv2/General/?groupId=72602e12-78ac-474c-99d6-f619710353a9",
			want: Ref{
				Kind:        KindChannel,
				Raw:         "https://teams.microsoft.com/l/channel/19:9be3de4e70874c71a608dee9ba803ed3@thread.tacv2/General/?groupId=72602e12-78ac-474c-99d6-f619710353a9",
				TeamID:      "72602e12-78ac-474c-99d6-f619710353a9",
				ChannelID:   "19:9be3de4e70874c71a608dee9ba803ed3@thread.tacv2",
				ChannelName: "General",
				Host:        "teams.microsoft.com",
			},
			ok: true,
		},
		{
			name: "documented team example (percent-encoded channel id in the path)",
			raw:  "https://teams.microsoft.com/l/team/19%3ATWLPKo8lD4v8zDxyw4FnDYY-ovnBJG5CSjmrHUAoOz41%40thread.tacv2/conversations?groupId=72602e12-78ac-474c-99d6-f619710353a9&tenantId=aaaabbbb-0000-cccc-1111-dddd2222eeee",
			want: Ref{
				Kind:      KindTeam,
				Raw:       "https://teams.microsoft.com/l/team/19%3ATWLPKo8lD4v8zDxyw4FnDYY-ovnBJG5CSjmrHUAoOz41%40thread.tacv2/conversations?groupId=72602e12-78ac-474c-99d6-f619710353a9&tenantId=aaaabbbb-0000-cccc-1111-dddd2222eeee",
				TeamID:    "72602e12-78ac-474c-99d6-f619710353a9",
				ChannelID: "19:TWLPKo8lD4v8zDxyw4FnDYY-ovnBJG5CSjmrHUAoOz41@thread.tacv2",
				Host:      "teams.microsoft.com",
				TenantID:  "aaaabbbb-0000-cccc-1111-dddd2222eeee",
			},
			ok: true,
		},
		{
			name: "team form with shuffled parameters",
			raw:  "https://teams.microsoft.com/l/team/19:TWLPKo8lD4v8zDxyw4FnDYY-ovnBJG5CSjmrHUAoOz41@thread.tacv2/conversations?tenantId=aaaabbbb-0000-cccc-1111-dddd2222eeee&groupId=72602e12-78ac-474c-99d6-f619710353a9",
			want: Ref{
				Kind:      KindTeam,
				Raw:       "https://teams.microsoft.com/l/team/19:TWLPKo8lD4v8zDxyw4FnDYY-ovnBJG5CSjmrHUAoOz41@thread.tacv2/conversations?tenantId=aaaabbbb-0000-cccc-1111-dddd2222eeee&groupId=72602e12-78ac-474c-99d6-f619710353a9",
				TeamID:    "72602e12-78ac-474c-99d6-f619710353a9",
				ChannelID: "19:TWLPKo8lD4v8zDxyw4FnDYY-ovnBJG5CSjmrHUAoOz41@thread.tacv2",
				Host:      "teams.microsoft.com",
				TenantID:  "aaaabbbb-0000-cccc-1111-dddd2222eeee",
			},
			ok: true,
		},
		{
			name: "documented chat example",
			raw:  "https://teams.microsoft.com/l/chat/19:c6d70e392a384916c3262b15406d763e@thread.v2/conversations",
			want: Ref{
				Kind:   KindChat,
				Raw:    "https://teams.microsoft.com/l/chat/19:c6d70e392a384916c3262b15406d763e@thread.v2/conversations",
				ChatID: "19:c6d70e392a384916c3262b15406d763e@thread.v2",
				Host:   "teams.microsoft.com",
			},
			ok: true,
		},
		{
			name: "documented compose chat example",
			raw:  "https://teams.microsoft.com/l/chat/0/0?tenantId=f4c2b8e1-7a3d-4e6f-9b12-8c5d0e3a1f47&users=joe@contoso.com,bob@contoso.com&topicName=Prep%20For%20Meeting%20Tomorrow&message=Hi%20folks%2C%20kicking%20off%20a%20chat%20about%20our%20meeting%20tomorrow",
			want: Ref{
				Kind: KindChat,
				Raw:  "https://teams.microsoft.com/l/chat/0/0?tenantId=f4c2b8e1-7a3d-4e6f-9b12-8c5d0e3a1f47&users=joe@contoso.com,bob@contoso.com&topicName=Prep%20For%20Meeting%20Tomorrow&message=Hi%20folks%2C%20kicking%20off%20a%20chat%20about%20our%20meeting%20tomorrow",
				Path: []string{"joe@contoso.com", "bob@contoso.com"},
				Host: "teams.microsoft.com",
			},
			ok: true,
		},
		{
			name: "documented bot compose chat example names one participant",
			raw:  "https://teams.microsoft.com/l/chat/0/0?users=28:47345678-2134-6534-9143-65146789012&message=This%20message%20was%20triggered%20by%20a%20link!",
			want: Ref{
				Kind: KindChat,
				Raw:  "https://teams.microsoft.com/l/chat/0/0?users=28:47345678-2134-6534-9143-65146789012&message=This%20message%20was%20triggered%20by%20a%20link!",
				Path: []string{"28:47345678-2134-6534-9143-65146789012"},
				User: "28:47345678-2134-6534-9143-65146789012",
				Host: "teams.microsoft.com",
			},
			ok: true,
		},
		{
			name: "documented file example",
			raw:  "https://teams.microsoft.com/l/file/5E0154FC-F2B4-4DA5-8CDA-F096E72C0A80?tenantId=0d9b645f-597b-41f0-a2a3-ef103fbd91bb&fileType=pptx&objectUrl=https%3A%2F%2Fmicrosoft.sharepoint.com%2Fteams%2FActionPlatform%2FShared%20Documents%2FFC7-%20Bot%20and%20Action%20Infra%2FKaizala%20Actions%20in%20Adaptive%20Cards%20-%20Deck.pptx&baseUrl=https%3A%2F%2Fmicrosoft.sharepoint.com%2Fteams%2FActionPlatform&serviceName=teams&threadId=19:f8fbfc4d89e24ef5b3b8692538cebeb7@thread.skype&groupId=ae063b79-5315-4ddb-ba70-27328ba6c31e",
			want: Ref{
				Kind:   KindFile,
				Raw:    "https://teams.microsoft.com/l/file/5E0154FC-F2B4-4DA5-8CDA-F096E72C0A80?tenantId=0d9b645f-597b-41f0-a2a3-ef103fbd91bb&fileType=pptx&objectUrl=https%3A%2F%2Fmicrosoft.sharepoint.com%2Fteams%2FActionPlatform%2FShared%20Documents%2FFC7-%20Bot%20and%20Action%20Infra%2FKaizala%20Actions%20in%20Adaptive%20Cards%20-%20Deck.pptx&baseUrl=https%3A%2F%2Fmicrosoft.sharepoint.com%2Fteams%2FActionPlatform&serviceName=teams&threadId=19:f8fbfc4d89e24ef5b3b8692538cebeb7@thread.skype&groupId=ae063b79-5315-4ddb-ba70-27328ba6c31e",
				FileID: "5E0154FC-F2B4-4DA5-8CDA-F096E72C0A80",
				TeamID: "ae063b79-5315-4ddb-ba70-27328ba6c31e",
				Host:   "teams.microsoft.com",
			},
			ok: true,
		},
		{
			name: "file form with unknown parameters and an extra path segment",
			raw:  "https://teams.microsoft.com/l/file/5E0154FC-F2B4-4DA5-8CDA-F096E72C0A80/preview?groupId=ae063b79-5315-4ddb-ba70-27328ba6c31e&unexpected=%3Cscript%3E",
			want: Ref{
				Kind:   KindFile,
				Raw:    "https://teams.microsoft.com/l/file/5E0154FC-F2B4-4DA5-8CDA-F096E72C0A80/preview?groupId=ae063b79-5315-4ddb-ba70-27328ba6c31e&unexpected=%3Cscript%3E",
				FileID: "5E0154FC-F2B4-4DA5-8CDA-F096E72C0A80",
				TeamID: "ae063b79-5315-4ddb-ba70-27328ba6c31e",
				Host:   "teams.microsoft.com",
			},
			ok: true,
		},
		{
			name: "message form with an extra path segment after the message id",
			raw:  "https://teams.microsoft.com/l/message/19:3997a8734ee5432bb9cdedb7c432ae7d@thread.tacv2/1648741500652/extra?groupId=3606f714-ec2e-41b3-9ad1-6afb331bd35d",
			want: Ref{
				Kind:            KindMessage,
				Raw:             "https://teams.microsoft.com/l/message/19:3997a8734ee5432bb9cdedb7c432ae7d@thread.tacv2/1648741500652/extra?groupId=3606f714-ec2e-41b3-9ad1-6afb331bd35d",
				TeamID:          "3606f714-ec2e-41b3-9ad1-6afb331bd35d",
				ChannelID:       "19:3997a8734ee5432bb9cdedb7c432ae7d@thread.tacv2",
				MessageID:       "1648741500652",
				ParentMessageID: "1648741500652",
				Host:            "teams.microsoft.com",
			},
			ok: true,
		},
		{
			name: "message form with shuffled parameters and an unknown parameter",
			raw:  "https://teams.microsoft.com/l/message/19:3997a8734ee5432bb9cdedb7c432ae7d@thread.tacv2/1648741500652?createdTime=1648741500652&unknown=ignored&channelName=General&groupId=3606f714-ec2e-41b3-9ad1-6afb331bd35d&teamName=Product%20Launch&parentMessageId=1648741500651&tenantId=f4c2b8e1-7a3d-4e6f-9b12-8c5d0e3a1f47",
			want: Ref{
				Kind:            KindMessage,
				Raw:             "https://teams.microsoft.com/l/message/19:3997a8734ee5432bb9cdedb7c432ae7d@thread.tacv2/1648741500652?createdTime=1648741500652&unknown=ignored&channelName=General&groupId=3606f714-ec2e-41b3-9ad1-6afb331bd35d&teamName=Product%20Launch&parentMessageId=1648741500651&tenantId=f4c2b8e1-7a3d-4e6f-9b12-8c5d0e3a1f47",
				TeamID:          "3606f714-ec2e-41b3-9ad1-6afb331bd35d",
				TeamName:        "Product Launch",
				ChannelID:       "19:3997a8734ee5432bb9cdedb7c432ae7d@thread.tacv2",
				ChannelName:     "General",
				MessageID:       "1648741500652",
				ParentMessageID: "1648741500651",
				Host:            "teams.microsoft.com",
				TenantID:        "f4c2b8e1-7a3d-4e6f-9b12-8c5d0e3a1f47",
			},
			ok: true,
		},
		{
			name: "message form with a trailing slash",
			raw:  "https://teams.microsoft.com/l/message/19:3997a8734ee5432bb9cdedb7c432ae7d@thread.tacv2/1648741500652/?groupId=3606f714-ec2e-41b3-9ad1-6afb331bd35d",
			want: Ref{
				Kind:            KindMessage,
				Raw:             "https://teams.microsoft.com/l/message/19:3997a8734ee5432bb9cdedb7c432ae7d@thread.tacv2/1648741500652/?groupId=3606f714-ec2e-41b3-9ad1-6afb331bd35d",
				TeamID:          "3606f714-ec2e-41b3-9ad1-6afb331bd35d",
				ChannelID:       "19:3997a8734ee5432bb9cdedb7c432ae7d@thread.tacv2",
				MessageID:       "1648741500652",
				ParentMessageID: "1648741500652",
				Host:            "teams.microsoft.com",
			},
			ok: true,
		},
		{
			name: "the newer cloud host is accepted defensively (PLAN.md:164)",
			raw:  "https://teams.cloud.microsoft/l/channel/19:9be3de4e70874c71a608dee9ba803ed3@thread.tacv2/General?groupId=72602e12-78ac-474c-99d6-f619710353a9&tenantId=aaaabbbb-0000-cccc-1111-dddd2222eeee&ngc=true",
			want: Ref{
				Kind:        KindChannel,
				Raw:         "https://teams.cloud.microsoft/l/channel/19:9be3de4e70874c71a608dee9ba803ed3@thread.tacv2/General?groupId=72602e12-78ac-474c-99d6-f619710353a9&tenantId=aaaabbbb-0000-cccc-1111-dddd2222eeee&ngc=true",
				TeamID:      "72602e12-78ac-474c-99d6-f619710353a9",
				ChannelID:   "19:9be3de4e70874c71a608dee9ba803ed3@thread.tacv2",
				ChannelName: "General",
				Host:        "teams.cloud.microsoft",
				TenantID:    "aaaabbbb-0000-cccc-1111-dddd2222eeee",
			},
			ok: true,
		},
		{
			name: "the msteams scheme is the second protocol handler",
			raw:  "msteams://teams.microsoft.com/l/channel/19:9be3de4e70874c71a608dee9ba803ed3@thread.tacv2/General?groupId=72602e12-78ac-474c-99d6-f619710353a9",
			want: Ref{
				Kind:        KindChannel,
				Raw:         "msteams://teams.microsoft.com/l/channel/19:9be3de4e70874c71a608dee9ba803ed3@thread.tacv2/General?groupId=72602e12-78ac-474c-99d6-f619710353a9",
				TeamID:      "72602e12-78ac-474c-99d6-f619710353a9",
				ChannelID:   "19:9be3de4e70874c71a608dee9ba803ed3@thread.tacv2",
				ChannelName: "General",
				Host:        "teams.microsoft.com",
			},
			ok: true,
		},
		{
			name: "uppercase scheme and host are normalized",
			raw:  "HTTPS://Teams.Microsoft.COM/l/chat/19:c6d70e392a384916c3262b15406d763e@thread.v2/conversations",
			want: Ref{
				Kind:   KindChat,
				Raw:    "HTTPS://Teams.Microsoft.COM/l/chat/19:c6d70e392a384916c3262b15406d763e@thread.v2/conversations",
				ChatID: "19:c6d70e392a384916c3262b15406d763e@thread.v2",
				Host:   "Teams.Microsoft.COM",
			},
			ok: true,
		},
		{
			name: "an empty groupId is the same as a missing one",
			raw:  "https://teams.microsoft.com/l/message/19:3997a8734ee5432bb9cdedb7c432ae7d@thread.tacv2/1648741500652?groupId=",
			want: Ref{
				Kind:            KindMessage,
				Raw:             "https://teams.microsoft.com/l/message/19:3997a8734ee5432bb9cdedb7c432ae7d@thread.tacv2/1648741500652?groupId=",
				ChannelID:       "19:3997a8734ee5432bb9cdedb7c432ae7d@thread.tacv2",
				MessageID:       "1648741500652",
				ParentMessageID: "1648741500652",
				Host:            "teams.microsoft.com",
			},
			ok: true,
		},
		{
			name: "a chat container id without a context reads as a chat message",
			raw:  "https://teams.microsoft.com/l/message/19:253f5895-9a62-4362-8d38-43f0205c702c_f1b94dcf-0aa3-4989-bcdf-ef4a5ed00f86@unq.gbl.spaces/1563480968434",
			want: Ref{
				Kind:      KindMessage,
				Raw:       "https://teams.microsoft.com/l/message/19:253f5895-9a62-4362-8d38-43f0205c702c_f1b94dcf-0aa3-4989-bcdf-ef4a5ed00f86@unq.gbl.spaces/1563480968434",
				ChatID:    "19:253f5895-9a62-4362-8d38-43f0205c702c_f1b94dcf-0aa3-4989-bcdf-ef4a5ed00f86@unq.gbl.spaces",
				MessageID: "1563480968434",
				InChat:    true,
				Host:      "teams.microsoft.com",
			},
			ok: true,
		},
		{
			name: "a non-chat context value keeps the channel reading",
			raw:  "https://teams.microsoft.com/l/message/19:3997a8734ee5432bb9cdedb7c432ae7d@thread.tacv2/1648741500652?context=%7B%22contextType%22:%22channel%22%7D&groupId=3606f714-ec2e-41b3-9ad1-6afb331bd35d",
			want: Ref{
				Kind:            KindMessage,
				Raw:             "https://teams.microsoft.com/l/message/19:3997a8734ee5432bb9cdedb7c432ae7d@thread.tacv2/1648741500652?context=%7B%22contextType%22:%22channel%22%7D&groupId=3606f714-ec2e-41b3-9ad1-6afb331bd35d",
				TeamID:          "3606f714-ec2e-41b3-9ad1-6afb331bd35d",
				ChannelID:       "19:3997a8734ee5432bb9cdedb7c432ae7d@thread.tacv2",
				MessageID:       "1648741500652",
				ParentMessageID: "1648741500652",
				Host:            "teams.microsoft.com",
			},
			ok: true,
		},
		{
			name: "a malformed context parameter is ignored, not an error",
			raw:  "https://teams.microsoft.com/l/message/19:3997a8734ee5432bb9cdedb7c432ae7d@thread.tacv2/1648741500652?context=not-json&groupId=3606f714-ec2e-41b3-9ad1-6afb331bd35d",
			want: Ref{
				Kind:            KindMessage,
				Raw:             "https://teams.microsoft.com/l/message/19:3997a8734ee5432bb9cdedb7c432ae7d@thread.tacv2/1648741500652?context=not-json&groupId=3606f714-ec2e-41b3-9ad1-6afb331bd35d",
				TeamID:          "3606f714-ec2e-41b3-9ad1-6afb331bd35d",
				ChannelID:       "19:3997a8734ee5432bb9cdedb7c432ae7d@thread.tacv2",
				MessageID:       "1648741500652",
				ParentMessageID: "1648741500652",
				Host:            "teams.microsoft.com",
			},
			ok: true,
		},
		{
			name: "a channel id that was percent-encoded twice is decoded once",
			raw:  "https://teams.microsoft.com/l/channel/19%253A9be3de4e70874c71a608dee9ba803ed3%2540thread.tacv2/General?groupId=72602e12-78ac-474c-99d6-f619710353a9",
			want: Ref{
				Kind:        KindChannel,
				Raw:         "https://teams.microsoft.com/l/channel/19%253A9be3de4e70874c71a608dee9ba803ed3%2540thread.tacv2/General?groupId=72602e12-78ac-474c-99d6-f619710353a9",
				TeamID:      "72602e12-78ac-474c-99d6-f619710353a9",
				ChannelID:   "19%3A9be3de4e70874c71a608dee9ba803ed3%40thread.tacv2",
				ChannelName: "General",
				Host:        "teams.microsoft.com",
			},
			ok: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, ok, err := ParseURL(tt.raw)
			if err != nil {
				t.Fatalf("ParseURL(%q) returned an unexpected error: %v", tt.raw, err)
			}
			if ok != tt.ok {
				t.Fatalf("ParseURL(%q) ok = %v, want %v", tt.raw, ok, tt.ok)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("ParseURL(%q)\n got = %#v; want %#v", tt.raw, got, tt.want)
			}
			if got.Raw != tt.raw {
				t.Errorf("Raw = %q, want the input %q", got.Raw, tt.raw)
			}
		})
	}
}

// TestParseURLRejectsNonMessageFamilies covers the families PLAN.md:164 names: the
// parser must report ok=true — so the caller raises a usage error (exit code 2)
// instead of falling through to the id parsers — and carry a non-nil error naming
// the family (/l/meeting* is three shapes: /l/meeting-join, /l/meeting-lobby and
// /l/meeting/…).
func TestParseURLRejectsNonMessageFamilies(t *testing.T) {
	t.Parallel()

	urls := []string{
		"https://teams.microsoft.com/l/app/0",
		"https://teams.microsoft.com/l/entity/28:47345678-2134-6534-9143-65146789012/index",
		"https://teams.microsoft.com/l/task/47345678-2134-6534-9143-65146789012",
		"https://teams.microsoft.com/l/call/0/0?users=joe@contoso.com",
		"https://teams.microsoft.com/l/meeting-join/47345678-2134-6534-9143-65146789012",
		"https://teams.microsoft.com/l/meeting-lobby/47345678-2134-6534-9143-65146789012",
		"https://teams.microsoft.com/l/meeting/47345678-2134-6534-9143-65146789012/conversations",
	}
	for _, raw := range urls {
		t.Run(raw, func(t *testing.T) {
			t.Parallel()
			got, ok, err := ParseURL(raw)
			if !ok {
				t.Errorf("ParseURL(%q) ok = false; a rejected Teams family must report ok", raw)
			}
			if err == nil {
				t.Fatalf("ParseURL(%q) returned no error", raw)
			}
			if got.Raw != raw {
				t.Errorf("Raw = %q, want the input", got.Raw)
			}
			if got.Host != "teams.microsoft.com" {
				t.Errorf("Host = %q, want the link's host", got.Host)
			}
		})
	}
}

// TestParseURLRejectsIncompleteReferences covers links that are shaped like a
// family the CLI does open but that carry no id to resolve. They are still
// "a Teams URL" for the caller's purposes (ok), so the error — not a silent
// fall-through to the id parsers — is what stops the command.
func TestParseURLRejectsIncompleteReferences(t *testing.T) {
	t.Parallel()

	urls := []string{
		"https://teams.microsoft.com/l/message",
		"https://teams.microsoft.com/l/message/19:3997a8734ee5432bb9cdedb7c432ae7d@thread.tacv2",
		"https://teams.microsoft.com/l/channel",
		"https://teams.microsoft.com/l/team",
		"https://teams.microsoft.com/l/chat",
		"https://teams.microsoft.com/l/file",
	}
	for _, raw := range urls {
		t.Run(raw, func(t *testing.T) {
			t.Parallel()
			if _, ok, err := ParseURL(raw); !ok || err == nil {
				t.Errorf("ParseURL(%q) = ok %v, err %v; want ok and a usage error", raw, ok, err)
			}
		})
	}
}

// TestParseURLIgnoresNonTeamsStrings covers the strings the parser must leave to
// the other reference shapes: PLAN.md:165 (a name path), PLAN.md:166-167 (a person
// or an e-mail) and PLAN.md:169 (a raw id).
func TestParseURLIgnoresNonTeamsStrings(t *testing.T) {
	t.Parallel()

	inputs := []string{
		"",
		"   ",
		"19:3997a8734ee5432bb9cdedb7c432ae7d@thread.tacv2",
		"1648741500652",
		"Engineering/General",
		"Engineering/General/1758000000000",
		"alice@colorkrew.com",
		"@alice",
		"https://teams.microsoft.com/",
		"https://teams.microsoft.com/l/",
		"https://teams.microsoft.com/l/not-a-family/0",
		"https://teams.microsoft.com/teams/messages",
		"https://example.com/l/channel/19:9be3de4e70874c71a608dee9ba803ed3@thread.tacv2/General",
		// A look-alike host is not the Teams host.
		"https://teams.microsoft.com.evil.example/l/chat/19:c6d70e392a384916c3262b15406d763e@thread.v2/conversations",
		// Another protocol handler is not one of the two documented ones.
		"http://teams.microsoft.com/l/chat/19:c6d70e392a384916c3262b15406d763e@thread.v2/conversations",
		"https://teams.cloud.microsoft/",
		"//teams.microsoft.com/l/chat/19:c6d70e392a384916c3262b15406d763e@thread.v2/conversations",
		"not a url at all",
	}
	for _, raw := range inputs {
		t.Run(raw, func(t *testing.T) {
			t.Parallel()
			got, ok, err := ParseURL(raw)
			if ok {
				t.Errorf("ParseURL(%q) ok = true, want false", raw)
			}
			if err != nil {
				t.Errorf("ParseURL(%q) err = %v, want nil", raw, err)
			}
			if got.Raw != raw {
				t.Errorf("Raw = %q, want the input %q", got.Raw, raw)
			}
			if !got.IsZero() {
				t.Errorf("ParseURL(%q) = %#v, want a zero Ref", raw, got)
			}
		})
	}
}

// TestParseURLComposeChatWithoutUsers covers the /l/chat/0/0 form when the users
// parameter is missing: the two zeros are placeholders, so there is nothing to
// resolve and the string is not a usable deep link (ok=false, no error).
func TestParseURLComposeChatWithoutUsers(t *testing.T) {
	t.Parallel()

	got, ok, err := ParseURL("https://teams.microsoft.com/l/chat/0/0?topicName=Prep")
	if ok {
		t.Errorf("ok = true, want false for a compose link with no users")
	}
	if err != nil {
		t.Errorf("err = %v, want nil", err)
	}
	if got.Host != "teams.microsoft.com" {
		t.Errorf("Host = %q, want the link's host", got.Host)
	}
}

// TestParseURLChatComposeSkipsEmptyUsers pins the splitUsers tolerance: an empty
// entry between commas must not become a participant.
func TestParseURLChatComposeSkipsEmptyUsers(t *testing.T) {
	t.Parallel()

	got, ok, err := ParseURL("https://teams.microsoft.com/l/chat/0/0?users=joe@contoso.com,,bob@contoso.com,")
	if err != nil || !ok {
		t.Fatalf("ParseURL = ok %v, err %v", ok, err)
	}
	want := []string{"joe@contoso.com", "bob@contoso.com"}
	if !reflect.DeepEqual(got.Path, want) {
		t.Errorf("Path = %#v, want %#v", got.Path, want)
	}
	if got.User != "" {
		t.Errorf("User = %q, want empty for a two-person compose link", got.User)
	}
}

// unencodedChatMessageURL builds the documented chat-message link with the context
// parameter left unencoded (context={"contextType":"chat"}), the tolerance
// PLAN.md:164 requires in addition to the page's own percent-encoded example
// (refs/msteams/msteams-platform/concepts/build-and-test/deep-link-teams.md, "Deep
// link to navigate to chat messages").
func unencodedChatMessageURL() string {
	return "https://teams.microsoft.com" +
		"/l/message/19:253f5895-9a62-4362-8d38-43f0205c702c_f1b94dcf-0aa3-4989-bcdf-ef4a5ed00f86@unq.gbl.spaces/1563480968434" +
		"?context=" + `{"contextType":"chat"}`
}

// TestParseURLHostAndPathEdgeCases covers the defensive branches the table above
// does not reach: an explicit port and a look-alike host (refs/INDEX.md section 5
// accepts only the two documented hosts), an IPv6 authority, a malformed
// percent-escape (best-effort decoding, PLAN.md:164), a chat id without the 19:
// prefix, and a groupId that is escaped twice.
func TestParseURLHostAndPathEdgeCases(t *testing.T) {
	t.Parallel()

	t.Run("an explicit port is not part of the host", func(t *testing.T) {
		t.Parallel()
		got, ok, err := ParseURL("https://teams.microsoft.com:443/l/chat/19:c6d70e392a384916c3262b15406d763e@thread.v2/conversations")
		if err != nil || !ok {
			t.Fatalf("ok = %v, err = %v", ok, err)
		}
		if got.Kind != KindChat || got.ChatID != "19:c6d70e392a384916c3262b15406d763e@thread.v2" {
			t.Errorf("got %#v, want the documented chat id", got)
		}
	})

	t.Run("a non-numeric port is not stripped", func(t *testing.T) {
		t.Parallel()
		if _, ok, _ := ParseURL("https://teams.microsoft.com:abc/l/chat/19:c6d70e392a384916c3262b15406d763e@thread.v2/conversations"); ok {
			t.Error("a host with a non-numeric port is not the Teams host")
		}
	})

	t.Run("an IPv6 authority is not the Teams host", func(t *testing.T) {
		t.Parallel()
		if _, ok, _ := ParseURL("https://[::1]/l/chat/19:c6d70e392a384916c3262b15406d763e@thread.v2/conversations"); ok {
			t.Error("an IPv6 authority is not the Teams host")
		}
	})

	t.Run("a malformed percent-escape is kept verbatim", func(t *testing.T) {
		t.Parallel()
		// A malformed escape never reaches ParseURL as such (net/url rejects a raw
		// "%" that is not part of a valid escape), so the fallback is asserted
		// directly: the parser keeps the segment verbatim rather than failing.
		if got := unescapeSegment("19:abc%zz@thread.tacv2"); got != "19:abc%zz@thread.tacv2" {
			t.Errorf("unescapeSegment = %q, want the input verbatim", got)
		}
		got, ok, err := ParseURL("https://teams.microsoft.com/l/channel/19:abc%25zz@thread.tacv2/General?groupId=72602e12-78ac-474c-99d6-f619710353a9")
		if err != nil || !ok {
			t.Fatalf("ok = %v, err = %v", ok, err)
		}
		if got.ChannelID != "19:abc%zz@thread.tacv2" {
			t.Errorf("ChannelID = %q, want the segment verbatim", got.ChannelID)
		}
	})

	t.Run("a doubly escaped groupId is decoded once", func(t *testing.T) {
		t.Parallel()
		got, ok, err := ParseURL("https://teams.microsoft.com/l/channel/19:9be3de4e70874c71a608dee9ba803ed3@thread.tacv2/General?groupId=72602e12-78ac-474c-99d6-f619710353a9")
		if err != nil || !ok {
			t.Fatalf("ok = %v, err = %v", ok, err)
		}
		if got.TeamID != "72602e12-78ac-474c-99d6-f619710353a9" {
			t.Errorf("TeamID = %q", got.TeamID)
		}
	})

	t.Run("a container id without the 19: prefix is never a chat", func(t *testing.T) {
		t.Parallel()
		got, ok, err := ParseURL("https://teams.microsoft.com/l/message/18:abc@thread.tacv2/1648741500652")
		if err != nil || !ok {
			t.Fatalf("ok = %v, err = %v", ok, err)
		}
		if got.InChat || got.ChatID != "" {
			t.Errorf("got %#v, want the channel reading for a container id that is not 19:", got)
		}
	})

	t.Run("a container id that is not a chat id stays a channel message", func(t *testing.T) {
		t.Parallel()
		got, ok, err := ParseURL("https://teams.microsoft.com/l/message/19:3997a8734ee5432bb9cdedb7c432ae7d@thread.tacv2/1648741500652")
		if err != nil || !ok {
			t.Fatalf("ok = %v, err = %v", ok, err)
		}
		if got.InChat || got.ChatID != "" || got.ChannelID == "" {
			t.Errorf("got %#v, want the channel reading", got)
		}
	})
}

// FuzzParseURL asserts the three properties PLAN.md:164 and refs/INDEX.md section 5
// make the parser's contract: no input panics it, parsing is deterministic, and a
// channel-message link that carries a groupId always yields a non-empty TeamID.
func FuzzParseURL(f *testing.F) {
	seeds := []string{
		// The documented examples verbatim.
		"https://teams.microsoft.com/l/message/19:3997a8734ee5432bb9cdedb7c432ae7d@thread.tacv2/1648741500652?tenantId=f4c2b8e1-7a3d-4e6f-9b12-8c5d0e3a1f47&groupId=3606f714-ec2e-41b3-9ad1-6afb331bd35d&parentMessageId=1648741500652&teamName=Product%20Launch&channelName=General&createdTime=1648741500652",
		"https://teams.microsoft.com/l/message/19:253f5895-9a62-4362-8d38-43f0205c702c_f1b94dcf-0aa3-4989-bcdf-ef4a5ed00f86@unq.gbl.spaces/1563480968434?context=%7B%22contextType%22:%22chat%22%7D",
		"https://teams.microsoft.com/l/channel/19%3A9be3de4e70874c71a608dee9ba803ed3%40thread.tacv2/My%20example%20channel?groupId=72602e12-78ac-474c-99d6-f619710353a9&tenantId=aaaabbbb-0000-cccc-1111-dddd2222eeee&ngc=true&allowXTenantAccess=true",
		"https://teams.microsoft.com/l/team/19%3ATWLPKo8lD4v8zDxyw4FnDYY-ovnBJG5CSjmrHUAoOz41%40thread.tacv2/conversations?groupId=72602e12-78ac-474c-99d6-f619710353a9&tenantId=aaaabbbb-0000-cccc-1111-dddd2222eeee",
		"https://teams.microsoft.com/l/chat/0/0?tenantId=f4c2b8e1-7a3d-4e6f-9b12-8c5d0e3a1f47&users=joe@contoso.com,bob@contoso.com&topicName=Prep%20For%20Meeting%20Tomorrow",
		"https://teams.microsoft.com/l/file/5E0154FC-F2B4-4DA5-8CDA-F096E72C0A80?fileType=pptx&groupId=ae063b79-5315-4ddb-ba70-27328ba6c31e",
		// The newer host, which the mirror does not document (refs/INDEX.md
		// "teams.cloud.microsoft deep links"): PLAN.md:164 requires a fuzz case.
		"https://teams.cloud.microsoft/l/message/19:3997a8734ee5432bb9cdedb7c432ae7d@thread.tacv2/1648741500652?groupId=3606f714-ec2e-41b3-9ad1-6afb331bd35d",
		"https://teams.cloud.microsoft/l/channel/19%3A9be3de4e70874c71a608dee9ba803ed3%40thread.tacv2/General?groupId=72602e12-78ac-474c-99d6-f619710353a9",
		// The second protocol handler.
		"msteams://teams.microsoft.com/l/chat/19:c6d70e392a384916c3262b15406d763e@thread.v2/conversations",
		// The rejected families.
		"https://teams.microsoft.com/l/meeting-join/0",
		"https://teams.microsoft.com/l/app/0",
		// Shapes that are not deep links.
		"",
		"https://teams.microsoft.com",
		"Engineering/General",
		"19:3997a8734ee5432bb9cdedb7c432ae7d@thread.tacv2",
		"https://teams.microsoft.com/l/message/%zz/%zz?groupId=%zz",
	}
	for _, seed := range seeds {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, raw string) {
		ref, ok, err := ParseURL(raw)

		// 1. No input panics the parser; reaching this point proves it, and the
		// second pass proves parsing is deterministic.
		again, okAgain, errAgain := ParseURL(raw)
		if !reflect.DeepEqual(ref, again) {
			t.Fatalf("ParseURL(%q) is not deterministic: first = %#v, second = %#v", raw, ref, again)
		}
		if ok != okAgain {
			t.Fatalf("ParseURL(%q) ok is not deterministic: %v then %v", raw, ok, okAgain)
		}
		if (err == nil) != (errAgain == nil) {
			t.Fatalf("ParseURL(%q) error presence is not deterministic: %v then %v", raw, err, errAgain)
		}
		if ref.Raw != raw {
			t.Fatalf("ParseURL(%q) Raw = %q, want the input", raw, ref.Raw)
		}
		if !ok || err != nil {
			return
		}

		// 2. A channel-message link that carries a groupId always yields the team
		// id, because every channel-message Graph route needs it (PLAN.md:164).
		groupID := ""
		if u, parseErr := url.Parse(raw); parseErr == nil {
			groupID = u.Query().Get("groupId")
		}
		if ref.Kind == KindMessage && !ref.InChat && groupID != "" && ref.TeamID == "" {
			t.Fatalf("ParseURL(%q) dropped groupId %q from a channel message: %#v", raw, groupID, ref)
		}
	})
}
