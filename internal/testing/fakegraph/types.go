package fakegraph

import (
	"encoding/json"
	"time"
)

// This file holds the Microsoft Graph wire shapes the fake serves. Every shape
// mirrors the vendored api-reference under
// refs/graph/api-reference/v1.0/resources/ (cited per type); fields the
// reference marks read-only are still rendered because Graph returns them.

// graphTimeFormat renders an OData dateTimeOffset the way Graph examples do,
// for example 2022-07-15T07:01:01Z
// (refs/graph/concepts/search-concept-chat-messages.md:365-366).
const graphTimeFormat = "2006-01-02T15:04:05Z"

// graphTime renders t as a Graph timestamp, or "" for the zero time.
func graphTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(graphTimeFormat)
}

// graphTimePtr renders t as a Graph timestamp pointer, so the field is emitted
// as JSON null when unset. Graph does that for nullable timestamps such as
// chatMessage.deletedDateTime.
func graphTimePtr(t time.Time) *string {
	if t.IsZero() {
		return nil
	}
	s := graphTime(t)
	return &s
}

// identity mirrors microsoft.graph.identity: id and displayName only
// (refs/graph/api-reference/v1.0/resources/identityset.md).
type identity struct {
	ID          string `json:"id,omitempty"`
	DisplayName string `json:"displayName,omitempty"`
}

// identitySet mirrors microsoft.graph.identitySet
// (refs/graph/api-reference/v1.0/resources/identityset.md).
type identitySet struct {
	Application  *identity `json:"application,omitempty"`
	Conversation *identity `json:"conversation,omitempty"`
	Device       *identity `json:"device,omitempty"`
	User         *identity `json:"user,omitempty"`
}

// teamworkUserIdentity mirrors microsoft.graph.teamworkUserIdentity, which
// chatMessageMention.mentioned.user uses. userIdentityType is the value the
// mention writer must set to aadUser (PLAN.md, "Writing a message").
type teamworkUserIdentity struct {
	ID               string `json:"id,omitempty"`
	DisplayName      string `json:"displayName,omitempty"`
	TenantID         string `json:"tenantId,omitempty"`
	UserIdentityType string `json:"userIdentityType,omitempty"`
}

// mentionedIdentitySet mirrors microsoft.graph.chatMessageMentionedIdentitySet
// (refs/graph/api-reference/v1.0/resources/chatmessagemention.md).
type mentionedIdentitySet struct {
	Application  *identity             `json:"application,omitempty"`
	Conversation *identity             `json:"conversation,omitempty"`
	Tag          *identity             `json:"tag,omitempty"`
	User         *teamworkUserIdentity `json:"user,omitempty"`
}

// mentionWire mirrors microsoft.graph.chatMessageMention. id is the index that
// matches the <at id="{index}"> tag in the body
// (refs/graph/api-reference/v1.0/resources/chatmessagemention.md).
type mentionWire struct {
	ID          int                   `json:"id"`
	MentionText string                `json:"mentionText,omitempty"`
	Mentioned   *mentionedIdentitySet `json:"mentioned,omitempty"`
}

// itemBody mirrors microsoft.graph.itemBody
// (refs/graph/api-reference/v1.0/resources/itembody.md).
type itemBody struct {
	Content     string `json:"content"`
	ContentType string `json:"contentType,omitempty"`
}

// attachmentWire mirrors microsoft.graph.chatMessageAttachment
// (refs/graph/api-reference/v1.0/resources/chatmessageattachment.md).
type attachmentWire struct {
	Content      string `json:"content,omitempty"`
	ContentType  string `json:"contentType,omitempty"`
	ContentURL   string `json:"contentUrl,omitempty"`
	ID           string `json:"id,omitempty"`
	Name         string `json:"name,omitempty"`
	TeamsAppID   string `json:"teamsAppId,omitempty"`
	ThumbnailURL string `json:"thumbnailUrl,omitempty"`
}

// reactionWire mirrors microsoft.graph.chatMessageReaction
// (refs/graph/api-reference/v1.0/resources/chatmessagereaction.md).
type reactionWire struct {
	CreatedDateTime    string    `json:"createdDateTime,omitempty"`
	DisplayName        string    `json:"displayName,omitempty"`
	ReactionContentURL string    `json:"reactionContentUrl,omitempty"`
	ReactionType       string    `json:"reactionType,omitempty"`
	User               *identity `json:"user,omitempty"`
}

// hostedContentWire mirrors microsoft.graph.chatMessageHostedContent when it is
// listed. contentBytes is write-only, so it never appears in a list response
// (refs/graph/api-reference/v1.0/resources/chatmessagehostedcontent.md).
type hostedContentWire struct {
	ID          string `json:"id"`
	ContentType string `json:"contentType,omitempty"`
}

// channelIdentity mirrors microsoft.graph.channelIdentity, which a channel
// message carries (refs/graph/api-reference/v1.0/resources/chatmessage.md).
type channelIdentity struct {
	ChannelID string `json:"channelId,omitempty"`
	TeamID    string `json:"teamId,omitempty"`
}

// messageWire is the chatMessage resource Graph returns. Nullable timestamps
// are pointers so they render as null, which is what Graph does
// (refs/graph/api-reference/v1.0/resources/chatmessage.md).
type messageWire struct {
	ID                   string              `json:"id"`
	ReplyToID            string              `json:"replyToId,omitempty"`
	Etag                 string              `json:"etag,omitempty"`
	MessageType          string              `json:"messageType,omitempty"`
	CreatedDateTime      string              `json:"createdDateTime,omitempty"`
	LastModifiedDateTime string              `json:"lastModifiedDateTime,omitempty"`
	LastEditedDateTime   *string             `json:"lastEditedDateTime"`
	DeletedDateTime      *string             `json:"deletedDateTime"`
	Subject              string              `json:"subject,omitempty"`
	Summary              string              `json:"summary,omitempty"`
	Importance           string              `json:"importance,omitempty"`
	Locale               string              `json:"locale,omitempty"`
	WebURL               string              `json:"webUrl,omitempty"`
	From                 *identitySet        `json:"from,omitempty"`
	Body                 itemBody            `json:"body"`
	Attachments          []attachmentWire    `json:"attachments,omitempty"`
	Mentions             []mentionWire       `json:"mentions,omitempty"`
	Reactions            []reactionWire      `json:"reactions,omitempty"`
	HostedContents       []hostedContentWire `json:"hostedContents,omitempty"`
	ChannelIdentity      *channelIdentity    `json:"channelIdentity,omitempty"`
	ChatID               string              `json:"chatId,omitempty"`
	EventDetail          json.RawMessage     `json:"eventDetail,omitempty"`
	// Replies is only rendered for the channel message list with
	// $expand=replies; it is not a chatMessage property.
	Replies []messageWire `json:"replies,omitempty"`
	// RepliesNextLink carries replies@odata.nextLink when $expand=replies
	// inlines only the first page
	// (refs/graph/api-reference/v1.0/api/channel-list-messages.md:37-38).
	RepliesNextLink string `json:"replies@odata.nextLink,omitempty"`
}

// conversationMemberWire mirrors microsoft.graph.aadUserConversationMember
// (refs/graph/api-reference/v1.0/resources/aaduserconversationmember.md).
type conversationMemberWire struct {
	ODataType                   string   `json:"@odata.type,omitempty"`
	ID                          string   `json:"id"`
	DisplayName                 string   `json:"displayName,omitempty"`
	Email                       string   `json:"email,omitempty"`
	Roles                       []string `json:"roles,omitempty"`
	TenantID                    string   `json:"tenantId,omitempty"`
	UserID                      string   `json:"userId,omitempty"`
	VisibleHistoryStartDateTime string   `json:"visibleHistoryStartDateTime,omitempty"`
}

// channelWire mirrors microsoft.graph.channel
// (refs/graph/api-reference/v1.0/resources/channel.md).
type channelWire struct {
	ID              string `json:"id"`
	DisplayName     string `json:"displayName"`
	Description     string `json:"description,omitempty"`
	MembershipType  string `json:"membershipType"`
	CreatedDateTime string `json:"createdDateTime,omitempty"`
	WebURL          string `json:"webUrl,omitempty"`
}

// teamWire mirrors microsoft.graph.team, including the group-shared id
// (refs/graph/api-reference/v1.0/resources/team.md).
type teamWire struct {
	ID              string `json:"id"`
	DisplayName     string `json:"displayName"`
	Description     string `json:"description,omitempty"`
	Visibility      string `json:"visibility,omitempty"`
	CreatedDateTime string `json:"createdDateTime,omitempty"`
	WebURL          string `json:"webUrl,omitempty"`
	TenantID        string `json:"tenantId,omitempty"`
}

// chatViewpointWire mirrors microsoft.graph.chatViewpoint
// (refs/graph/api-reference/v1.0/resources/chatviewpoint.md).
type chatViewpointWire struct {
	LastMessageReadDateTime *string `json:"lastMessageReadDateTime"`
	IsHidden                bool    `json:"isHidden"`
}

// chatWire mirrors microsoft.graph.chat
// (refs/graph/api-reference/v1.0/resources/chat.md).
type chatWire struct {
	ID                  string                   `json:"id"`
	ChatType            string                   `json:"chatType"`
	Topic               *string                  `json:"topic"`
	CreatedDateTime     string                   `json:"createdDateTime,omitempty"`
	LastUpdatedDateTime string                   `json:"lastUpdatedDateTime,omitempty"`
	TenantID            string                   `json:"tenantId,omitempty"`
	WebURL              string                   `json:"webUrl,omitempty"`
	Viewpoint           *chatViewpointWire       `json:"viewpoint,omitempty"`
	Members             []conversationMemberWire `json:"members,omitempty"`
	// LastMessagePreview is rendered for $expand=lastMessagePreview.
	LastMessagePreview *messageWire `json:"lastMessagePreview,omitempty"`
}

// userWire mirrors the directory user subset the CLI reads. The default
// property set is the one user-list.md documents: businessPhones, displayName,
// givenName, id, jobTitle, mail, mobilePhone, officeLocation,
// preferredLanguage, surname and userPrincipalName
// (refs/graph/api-reference/v1.0/api/user-list.md:39).
type userWire struct {
	ID                string   `json:"id"`
	DisplayName       string   `json:"displayName,omitempty"`
	GivenName         string   `json:"givenName,omitempty"`
	Surname           string   `json:"surname,omitempty"`
	UserPrincipalName string   `json:"userPrincipalName,omitempty"`
	Mail              string   `json:"mail,omitempty"`
	JobTitle          string   `json:"jobTitle,omitempty"`
	Department        string   `json:"department,omitempty"`
	OfficeLocation    string   `json:"officeLocation,omitempty"`
	PreferredLanguage string   `json:"preferredLanguage,omitempty"`
	MobilePhone       string   `json:"mobilePhone,omitempty"`
	BusinessPhones    []string `json:"businessPhones,omitempty"`
}

// scoredEmailAddressWire mirrors microsoft.graph.scoredEmailAddress, which a
// person carries (refs/graph/api-reference/v1.0/resources/scoredemailaddress.md).
type scoredEmailAddressWire struct {
	Address string `json:"address,omitempty"`
	// RelevanceScore orders /me/people results
	// (refs/graph/api-reference/v1.0/api/user-list-people.md:12).
	RelevanceScore float64 `json:"relevanceScore"`
}

// personWire mirrors the subset of microsoft.graph.person that /me/people
// returns (refs/graph/api-reference/v1.0/resources/person.md).
type personWire struct {
	ID                   string                   `json:"id"`
	DisplayName          string                   `json:"displayName,omitempty"`
	GivenName            string                   `json:"givenName,omitempty"`
	Surname              string                   `json:"surname,omitempty"`
	CompanyName          string                   `json:"companyName,omitempty"`
	JobTitle             string                   `json:"jobTitle,omitempty"`
	Department           string                   `json:"department,omitempty"`
	OfficeLocation       string                   `json:"officeLocation,omitempty"`
	UserPrincipalName    string                   `json:"userPrincipalName,omitempty"`
	ScoredEmailAddresses []scoredEmailAddressWire `json:"scoredEmailAddresses,omitempty"`
}

// driveItemWire mirrors the driveItem subset the CLI reads: a channel's
// filesFolder, a folder's children and a file's metadata plus the
// pre-authenticated download URL
// (refs/graph/api-reference/v1.0/resources/driveitem.md,
// refs/graph/api-reference/v1.0/api/driveitem-get-content.md:24).
type driveItemWire struct {
	ID                   string         `json:"id"`
	Name                 string         `json:"name"`
	Size                 int64          `json:"size"`
	CreatedDateTime      string         `json:"createdDateTime,omitempty"`
	LastModifiedDateTime string         `json:"lastModifiedDateTime,omitempty"`
	WebURL               string         `json:"webUrl,omitempty"`
	ETag                 string         `json:"eTag,omitempty"`
	DownloadURL          string         `json:"@microsoft.graph.downloadUrl,omitempty"`
	File                 *fileFacet     `json:"file,omitempty"`
	Folder               *folderFacet   `json:"folder,omitempty"`
	ParentReference      *itemReference `json:"parentReference,omitempty"`
}

// fileFacet mirrors microsoft.graph.file.
type fileFacet struct {
	MimeType string `json:"mimeType,omitempty"`
	Hashes   *struct {
		QuickXorHash string `json:"quickXorHash,omitempty"`
	} `json:"hashes,omitempty"`
}

// folderFacet mirrors microsoft.graph.folder.
type folderFacet struct {
	ChildCount int `json:"childCount"`
}

// itemReference mirrors microsoft.graph.itemReference.
type itemReference struct {
	DriveID   string `json:"driveId,omitempty"`
	DriveType string `json:"driveType,omitempty"`
	ID        string `json:"id,omitempty"`
	Path      string `json:"path,omitempty"`
}

// searchRequestWire is one searchRequest
// (refs/graph/api-reference/v1.0/resources/searchrequest.md).
type searchRequestWire struct {
	EntityTypes []string `json:"entityTypes"`
	Query       struct {
		QueryString string `json:"queryString"`
	} `json:"query"`
	From   *int     `json:"from,omitempty"`
	Size   *int     `json:"size,omitempty"`
	Fields []string `json:"fields,omitempty"`
}

// searchEnvelopeWire is the POST /search/query request body
// (refs/graph/api-reference/v1.0/api/search-query.md:41).
type searchEnvelopeWire struct {
	Requests []searchRequestWire `json:"requests"`
}

// searchHitWire mirrors microsoft.graph.searchHit
// (refs/graph/api-reference/v1.0/resources/searchhit.md). resource is rendered
// without a body, which is what the spike observed
// (docs/spike/phase1.md:83).
type searchHitWire struct {
	HitID    string          `json:"hitId"`
	Rank     int             `json:"rank"`
	Summary  string          `json:"summary,omitempty"`
	Resource json.RawMessage `json:"resource"`
}

// searchHitsContainerWire mirrors microsoft.graph.searchHitsContainer
// (refs/graph/api-reference/v1.0/resources/searchhitscontainer.md). total is
// the full match count, which is what the spike observed, not the page count
// the docs describe (docs/spike/phase1.md:77).
type searchHitsContainerWire struct {
	Hits                 []searchHitWire `json:"hits"`
	Total                int             `json:"total"`
	MoreResultsAvailable bool            `json:"moreResultsAvailable"`
}

// searchResponseWire mirrors microsoft.graph.searchResponse
// (refs/graph/api-reference/v1.0/resources/searchresponse.md).
type searchResponseWire struct {
	SearchTerms    []string                  `json:"searchTerms"`
	HitsContainers []searchHitsContainerWire `json:"hitsContainers"`
}

// searchResultWire is the top-level {"value":[…]}
// (refs/graph/concepts/search-concept-chat-messages.md:345).
type searchResultWire struct {
	Value []searchResponseWire `json:"value"`
}

// batchRequestEnvelopeWire is the $batch request body
// (refs/graph/concepts/json-batching.md:44-53).
type batchRequestEnvelopeWire struct {
	Requests []batchRequestItemWire `json:"requests"`
}

// batchRequestItemWire is one sub-request. dependsOn is documented at
// refs/graph/concepts/json-batching.md:251.
type batchRequestItemWire struct {
	ID        string            `json:"id"`
	Method    string            `json:"method"`
	URL       string            `json:"url"`
	Headers   map[string]string `json:"headers,omitempty"`
	Body      json.RawMessage   `json:"body,omitempty"`
	DependsOn []string          `json:"dependsOn,omitempty"`
}

// batchResponseEnvelopeWire is the outer 200 response
// (refs/graph/concepts/json-batching.md:119).
type batchResponseEnvelopeWire struct {
	Responses []batchResponseItemWire `json:"responses"`
}

// batchResponseItemWire is one sub-response; a failure arrives with its own
// status and a full error envelope while the outer call stays 200
// (refs/graph/concepts/json-batching.md:119-130).
type batchResponseItemWire struct {
	ID      string            `json:"id"`
	Status  int               `json:"status"`
	Headers map[string]string `json:"headers,omitempty"`
	Body    json.RawMessage   `json:"body,omitempty"`
}

// collectionWire is a plain OData collection page.
type collectionWire[T any] struct {
	Value    []T    `json:"value"`
	NextLink string `json:"@odata.nextLink,omitempty"`
}

// errorEnvelopeWire is the documented Graph error body
// (refs/graph/concepts/throttling.md:59-71).
type errorEnvelopeWire struct {
	Error errorBodyWire `json:"error"`
}

// errorBodyWire mirrors error.code/message/innerError.
type errorBodyWire struct {
	Code       string          `json:"code,omitempty"`
	Message    string          `json:"message,omitempty"`
	Target     string          `json:"target,omitempty"`
	InnerError *innerErrorWire `json:"innerError,omitempty"`
}

// innerErrorWire mirrors the documented innerError object. request-id and
// client-request-id are echoed from the request headers.
type innerErrorWire struct {
	Code            string `json:"code,omitempty"`
	Date            string `json:"date,omitempty"`
	Message         string `json:"message,omitempty"`
	RequestID       string `json:"request-id,omitempty"`
	ClientRequestID string `json:"client-request-id,omitempty"`
	Status          string `json:"status,omitempty"`
	RetryAfter      string `json:"retry-after,omitempty"`
}
