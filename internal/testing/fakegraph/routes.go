package fakegraph

// This file is the route table. Each entry carries the delegated scope the
// endpoint's api-reference page lists as least privileged (or its documented
// higher-privileged alternatives), taken from refs/INDEX.md section 1 and the
// shared refs/graph/api-reference/v1.0/includes/permissions/ tables.
//
// The route surface mirrors refs/INDEX.md section 1, which is also the list the
// Layer 6 contract tests are generated from, so the fake and the contract
// validation cannot drift apart silently.

// chatPrefixes are the three documented chat containers: /me/chats,
// /chats and /users/{id}/chats. Every chat route is registered under each, so
// the CLI is exercised whichever form it sends
// (refs/INDEX.md "Chats"; docs/spike/phase1.md:99).
var chatPrefixes = []string{"/me/chats", "/chats", "/users/{user-id}/chats"}

// buildRoutes returns the route table. Ambiguous patterns must never be
// registered twice for the same method and shape; [Server.serve] panics if two
// match.
func buildRoutes() []routeDef {
	routes := []routeDef{
		// Teams, channels (refs/INDEX.md "Channels and messages").
		{method: "GET", pattern: "/me", scopes: []string{"User.Read"}, fn: handleMe},
		{method: "GET", pattern: "/me/joinedTeams", scopes: []string{"Team.ReadBasic.All"}, fn: handleJoinedTeams},
		{method: "GET", pattern: "/teams/{team-id}", scopes: []string{"Team.ReadBasic.All"}, fn: handleGetTeam},
		{method: "GET", pattern: "/teams/{team-id}/members", scopes: []string{"TeamMember.Read.All"}, fn: handleTeamMembers},
		{method: "GET", pattern: "/teams/{team-id}/channels", scopes: []string{"Channel.ReadBasic.All"}, fn: handleListChannels},
		{method: "GET", pattern: "/teams/{team-id}/channels/{channel-id}", scopes: []string{"Channel.ReadBasic.All"}, fn: handleGetChannel},
		{method: "GET", pattern: "/teams/{team-id}/channels/{channel-id}/members", scopes: []string{"ChannelMember.Read.All"}, fn: handleChannelMembers},

		// Channel messages and replies. The read scope is ChannelMessage.Read.All
		// with Group.Read.All as the documented alternative; edit and delete need
		// ChannelMessage.ReadWrite, which the spike confirmed after
		// ChannelMessage.Edit was refused with 403 (docs/spike/phase1.md:27).
		{method: "GET", pattern: "/teams/{team-id}/channels/{channel-id}/messages", scopes: channelReadScopes, fn: handleChannelMessages},
		{method: "POST", pattern: "/teams/{team-id}/channels/{channel-id}/messages", scopes: channelSendScopes, fn: handlePostChannelMessage},
		{method: "GET", pattern: "/teams/{team-id}/channels/{channel-id}/messages/{chatMessage-id}", scopes: channelReadScopes, fn: handleChannelMessage},
		{method: "PATCH", pattern: "/teams/{team-id}/channels/{channel-id}/messages/{chatMessage-id}", scopes: channelWriteScopes, fn: handlePatchMessage},
		{method: "GET", pattern: "/teams/{team-id}/channels/{channel-id}/messages/{chatMessage-id}/replies", scopes: channelReadScopes, fn: handleReplies},
		{method: "POST", pattern: "/teams/{team-id}/channels/{channel-id}/messages/{chatMessage-id}/replies", scopes: channelSendScopes, fn: handlePostReply},
		{method: "GET", pattern: "/teams/{team-id}/channels/{channel-id}/messages/{chatMessage-id}/replies/{reply-id}", scopes: channelReadScopes, fn: handleReply},
		{method: "PATCH", pattern: "/teams/{team-id}/channels/{channel-id}/messages/{chatMessage-id}/replies/{reply-id}", scopes: channelWriteScopes, fn: handlePatchMessage},
		{method: "POST", pattern: "/teams/{team-id}/channels/{channel-id}/messages/{chatMessage-id}/softDelete", scopes: channelWriteScopes, fn: handleSoftDeleteMessage},
		{method: "POST", pattern: "/teams/{team-id}/channels/{channel-id}/messages/{chatMessage-id}/replies/{reply-id}/softDelete", scopes: channelWriteScopes, fn: handleSoftDeleteMessage},
		{method: "POST", pattern: "/teams/{team-id}/channels/{channel-id}/messages/{chatMessage-id}/setReaction", scopes: channelSendScopes, fn: handleSetReaction},
		{method: "POST", pattern: "/teams/{team-id}/channels/{channel-id}/messages/{chatMessage-id}/unsetReaction", scopes: channelSendScopes, fn: handleUnsetReaction},
		{method: "POST", pattern: "/teams/{team-id}/channels/{channel-id}/messages/{chatMessage-id}/replies/{reply-id}/setReaction", scopes: channelSendScopes, fn: handleSetReaction},
		{method: "POST", pattern: "/teams/{team-id}/channels/{channel-id}/messages/{chatMessage-id}/replies/{reply-id}/unsetReaction", scopes: channelSendScopes, fn: handleUnsetReaction},

		// Inline images: listed here and read through
		// .../hostedContents/{id}/$value. There is no v1.0 create endpoint for
		// hosted content, so the standalone POST is 405 — which is exactly what
		// the spike saw for the MCP's broken call (docs/spike/phase1.md:91;
		// refs/INDEX.md, "Inline images have no create endpoint in the v1.0 API
		// reference").
		{method: "GET", pattern: "/teams/{team-id}/channels/{channel-id}/messages/{chatMessage-id}/hostedContents", scopes: channelReadScopes, fn: handleListHostedContents},
		{method: "GET", pattern: "/teams/{team-id}/channels/{channel-id}/messages/{chatMessage-id}/hostedContents/{hosted-content-id}/$value", scopes: channelReadScopes, fn: handleGetHostedContentValue},
		{method: "GET", pattern: "/teams/{team-id}/channels/{channel-id}/messages/{chatMessage-id}/replies/{reply-id}/hostedContents", scopes: channelReadScopes, fn: handleListHostedContents},
		{method: "GET", pattern: "/teams/{team-id}/channels/{channel-id}/messages/{chatMessage-id}/replies/{reply-id}/hostedContents/{hosted-content-id}/$value", scopes: channelReadScopes, fn: handleGetHostedContentValue},
		{method: "POST", pattern: "/teams/{team-id}/channels/{channel-id}/messages/{chatMessage-id}/hostedContents", notAllowed: true},
		{method: "POST", pattern: "/teams/{team-id}/channels/{channel-id}/messages/{chatMessage-id}/replies/{reply-id}/hostedContents", notAllowed: true},

		// Search, users, people (refs/INDEX.md "Search, mentions, users").
		// /search/query is multi-resource; for chatMessage the documented scopes
		// are Chat.Read, Chat.ReadWrite and ChannelMessage.Read.All
		// (refs/graph/api-reference/v1.0/resources/search-api-overview.md,
		// refs/INDEX.md's search note).
		{method: "POST", pattern: "/search/query", scopes: []string{"Chat.Read", "Chat.ReadWrite", "ChannelMessage.Read.All"}, fn: handleSearch},
		{method: "GET", pattern: "/users", scopes: []string{"User.ReadBasic.All"}, fn: handleListUsers},
		{method: "GET", pattern: "/users/{user-id}", scopes: []string{"User.ReadBasic.All"}, fn: handleGetUser},
		{method: "GET", pattern: "/me/people", scopes: []string{"People.Read"}, fn: handlePeople},

		// Chats.
		{method: "POST", pattern: "/chats", scopes: []string{"Chat.Create"}, fn: handleCreateChat},
		{method: "DELETE", pattern: "/chats/{chat-id}", scopes: []string{"Chat.ManageDeletion.All"}, fn: handleDeleteChat},
		// The chat-level soft delete is documented through /users/{id}/chats —
		// the /chats form answers 405 in the live service
		// (docs/spike/phase1.md:99; PLAN.md "Chat quirks").
		{method: "POST", pattern: "/chats/{chat-id}/softDelete", notAllowed: true},
		// The working form of the chat soft delete, under /users/{id}/chats
		// (refs/graph/api-reference/v1.0/api/chatmessage-softdelete.md;
		// docs/spike/phase1.md:99).
		{method: "POST", pattern: "/users/{user-id}/chats/{chat-id}/softDelete", scopes: []string{"Chat.ReadWrite"}, fn: handleSoftDeleteChat},

		// Files.
		{method: "GET", pattern: "/teams/{team-id}/channels/{channel-id}/filesFolder", scopes: []string{"Files.Read.All", "ChannelSettings.Read.All", "Sites.Read.All"}, fn: handleFilesFolder},
		{method: "GET", pattern: "/drives/{drive-id}/items/{driveItem-id}", scopes: filesReadScopes, fn: handleGetDriveItem},
		{method: "GET", pattern: "/drives/{drive-id}/items/{driveItem-id}/children", scopes: filesReadScopes, fn: handleDriveItemChildren},
		{method: "GET", pattern: "/drives/{drive-id}/items/{driveItem-id}/content", scopes: filesReadScopes, fn: handleGetDriveItemContent},
		{method: "PUT", pattern: "/drives/{drive-id}/items/{driveItem-id}/content", scopes: []string{"Files.ReadWrite.All", "Files.ReadWrite", "Sites.ReadWrite.All"}, fn: handlePutDriveItemContent},
		{method: "POST", pattern: "/drives/{drive-id}/items/{driveItem-id}/createUploadSession", scopes: []string{"Files.ReadWrite.All", "Files.ReadWrite"}, fn: handleCreateUploadSession},
		// The pre-authenticated download URL and the upload session URL are
		// served by a different host in the live service and are not part of
		// the api-reference, so they are not in the contract route list. They
		// exist so /content's documented 302 and the upload session's chunks
		// resolve inside the fake.
		{method: "GET", pattern: "/_download/{drive-id}/{driveItem-id}", fn: handleDownload},
		{method: "PUT", pattern: "/_upload/{upload-id}", fn: handleUploadChunk},

		// $batch. Sub-requests are checked against their own route's scope, so
		// the batch itself requires none (refs/graph/concepts/json-batching.md).
		{method: "POST", pattern: "/$batch", fn: handleBatch},
	}

	for _, prefix := range chatPrefixes {
		routes = append(routes, chatRoutes(prefix)...)
	}
	return routes
}

// Scope groups reused by the routes above.
var (
	channelReadScopes  = []string{"ChannelMessage.Read.All", "Group.Read.All", "Group.ReadWrite.All"}
	channelSendScopes  = []string{"ChannelMessage.Send"}
	channelWriteScopes = []string{"ChannelMessage.ReadWrite", "Group.ReadWrite.All"}
	filesReadScopes    = []string{"Files.Read.All", "Files.Read", "Sites.Read.All"}
)

// chatRoutes returns every chat route under one prefix. The read scope differs
// per endpoint: chat metadata is Chat.ReadBasic, messages are Chat.Read, writes
// and the read-state actions are Chat.ReadWrite, and sending is
// ChatMessage.Send (refs/INDEX.md "Chats" and "Channels and messages").
func chatRoutes(prefix string) []routeDef {
	return []routeDef{
		{method: "GET", pattern: prefix, scopes: []string{"Chat.ReadBasic", "Chat.Read", "Chat.ReadWrite"}, fn: handleListChats},
		{method: "GET", pattern: prefix + "/{chat-id}", scopes: []string{"Chat.ReadBasic", "Chat.Read", "Chat.ReadWrite"}, fn: handleGetChat},
		{method: "GET", pattern: prefix + "/{chat-id}/members", scopes: []string{"Chat.ReadBasic", "Chat.Read", "Chat.ReadWrite"}, fn: handleChatMembers},
		{method: "POST", pattern: prefix + "/{chat-id}/members", scopes: []string{"ChatMember.ReadWrite", "Chat.ReadWrite"}, fn: handleAddChatMember},
		{method: "GET", pattern: prefix + "/{chat-id}/messages", scopes: []string{"Chat.Read", "Chat.ReadWrite"}, fn: handleChatMessages},
		{method: "POST", pattern: prefix + "/{chat-id}/messages", scopes: []string{"ChatMessage.Send", "Chat.ReadWrite"}, fn: handlePostChatMessage},
		{method: "POST", pattern: prefix + "/{chat-id}/messages/replyWithQuote", scopes: []string{"ChatMessage.Send"}, fn: handleReplyWithQuote},
		{method: "GET", pattern: prefix + "/{chat-id}/messages/{chatMessage-id}", scopes: []string{"Chat.Read", "Chat.ReadWrite"}, fn: handleChatMessage},
		{method: "PATCH", pattern: prefix + "/{chat-id}/messages/{chatMessage-id}", scopes: []string{"Chat.ReadWrite"}, fn: handlePatchMessage},
		{method: "POST", pattern: prefix + "/{chat-id}/messages/{chatMessage-id}/softDelete", scopes: []string{"Chat.ReadWrite"}, fn: handleSoftDeleteMessage},
		{method: "POST", pattern: prefix + "/{chat-id}/messages/{chatMessage-id}/setReaction", scopes: []string{"Chat.ReadWrite", "ChatMessage.Send"}, fn: handleSetReaction},
		{method: "POST", pattern: prefix + "/{chat-id}/messages/{chatMessage-id}/unsetReaction", scopes: []string{"Chat.ReadWrite", "ChatMessage.Send"}, fn: handleUnsetReaction},
		{method: "GET", pattern: prefix + "/{chat-id}/messages/{chatMessage-id}/hostedContents", scopes: []string{"Chat.Read", "Chat.ReadWrite"}, fn: handleListHostedContents},
		{method: "GET", pattern: prefix + "/{chat-id}/messages/{chatMessage-id}/hostedContents/{hosted-content-id}/$value", scopes: []string{"Chat.Read", "Chat.ReadWrite"}, fn: handleGetHostedContentValue},
		{method: "POST", pattern: prefix + "/{chat-id}/messages/{chatMessage-id}/hostedContents", notAllowed: true},
		{method: "POST", pattern: prefix + "/{chat-id}/markChatReadForUser", scopes: []string{"Chat.ReadWrite"}, fn: handleMarkChatRead},
		{method: "POST", pattern: prefix + "/{chat-id}/markChatUnreadForUser", scopes: []string{"Chat.ReadWrite"}, fn: handleMarkChatUnread},
	}
}
