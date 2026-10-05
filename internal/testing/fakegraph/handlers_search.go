package fakegraph

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"
)

// This file implements POST /search/query over the stored messages.
//
// The protocol is documented in
// refs/graph/api-reference/v1.0/resources/search-api-overview.md and
// refs/graph/concepts/search-concept-chat-messages.md. Where the live service
// disagrees with that page, the spike wins:
//
//   - `size` is honoured up to 50 for chatMessage; there is no 25 cap
//     (docs/spike/phase1.md:75);
//   - `total` is the full match count, not the page count
//     (docs/spike/phase1.md:77);
//   - hits carry no body (docs/spike/phase1.md:83);
//   - `sent` honours a time of day, so full UTC timestamps are compared
//     (docs/spike/phase1.md:80);
//   - `IsRead` returns 500 every time, whatever its casing, so the fake
//     reproduces that instead of pretending the term works
//     (docs/spike/phase1.md:79).

// MaxSearchSize is the largest `size` the fake honours for chatMessage. The
// spike observed 50 working and the docs cap `message` at 25, so 50 is the
// live behaviour (docs/spike/phase1.md:75).
const MaxSearchSize = 50

// The API has no @odata.nextLink, so the client pages by `from`/`size` itself.
// The docs require the first page to start at zero
// (refs/graph/api-reference/v1.0/resources/search-api-overview.md:67); the fake
// enforces that by remembering which queries have served a from=0 page.

// handleSearch serves POST /search/query.
func handleSearch(c *handlerCtx) {
	var env searchEnvelopeWire
	if !c.decodeBody(&env) {
		return
	}
	if len(env.Requests) != 1 {
		// "the service currently supports only a single searchRequest at a
		// time" (refs/graph/api-reference/v1.0/resources/search-api-overview.md:182).
		c.fail(badRequestf("The search request must contain exactly one searchRequest, got %d.", len(env.Requests)))
		return
	}
	req := env.Requests[0]
	// An event search is a different query language and a different result
	// shape, so it is served from handlers_calendar.go (Phase 6).
	if handleEventSearch(c, req) {
		return
	}
	if len(req.EntityTypes) != 1 || !strings.EqualFold(req.EntityTypes[0], "chatMessage") {
		// chatMessage cannot be mixed with another entity type
		// (refs/graph/api-reference/v1.0/resources/search-api-overview.md:184;
		// refs/INDEX.md's search note).
		c.fail(badRequestf("Only entityTypes ['chatMessage'] is supported, got %v.", req.EntityTypes))
		return
	}
	queryString := strings.TrimSpace(req.Query.QueryString)
	if queryString == "" {
		c.fail(badRequestf("The search request needs query.queryString."))
		return
	}
	from := 0
	if req.From != nil {
		from = *req.From
	}
	if from < 0 {
		c.fail(badRequestf("'from' must not be negative, got %d.", from))
		return
	}
	size := c.s.opts.SearchPageSize
	if req.Size != nil {
		size = *req.Size
	}
	if size < 1 {
		c.fail(badRequestf("'size' must be positive, got %d.", size))
		return
	}
	if size > MaxSearchSize {
		// The spike saw size=50 work and there is no documented 25 cap for
		// chatMessage (docs/spike/phase1.md:75).
		c.fail(badRequestf("'size' %d exceeds the maximum of %d for entityTypes 'chatMessage'.", size, MaxSearchSize))
		return
	}

	kql, kerr := parseKQL(queryString)
	if kerr != nil {
		c.fail(kerr)
		return
	}

	key := strings.ToLower(queryString)
	c.s.searchMu.Lock()
	seen := c.s.seenSearches[key]
	if from != 0 && !seen {
		c.s.searchMu.Unlock()
		c.fail(badRequestf("'from' must be 0 on the first page of a search (got %d).", from))
		return
	}
	if from == 0 {
		c.s.seenSearches[key] = true
	}
	c.s.searchMu.Unlock()

	st := c.s.st
	st.mu.RLock()
	matched := st.searchMessages(kql)
	st.mu.RUnlock()

	total := len(matched)
	page := []*messageRecord{}
	if from < total {
		end := from + size
		if end > total {
			end = total
		}
		page = matched[from:end]
	}
	hits := make([]searchHitWire, 0, len(page))
	for i, m := range page {
		hits = append(hits, searchHitWire{
			HitID:    m.id,
			Rank:     from + i + 1,
			Summary:  firstNonEmpty(m.summary, summaryOf(m.body.Content)),
			Resource: st.searchResource(m),
		})
	}
	// searchTerms is a non-nullable array in the description, so a query that
	// carries only scope terms (IsMentioned:true, for example) still sends [].
	terms := kql.freeText
	if terms == nil {
		terms = []string{}
	}
	c.json(http.StatusOK, searchResultWire{Value: []searchResponseWire{{
		SearchTerms: terms,
		HitsContainers: []searchHitsContainerWire{{
			Hits:  hits,
			Total: total,
			// total is the full match count here (spike), so "more results"
			// means the page did not reach the end of that count.
			MoreResultsAvailable: from+len(hits) < total,
		}},
	}}})
}

// searchMessages returns every message the query matches, in insertion order
// (the API does not sort messages, search-api-overview.md:195).
func (s *store) searchMessages(kql *kqlQuery) []*messageRecord {
	var out []*messageRecord
	for _, teamID := range s.teamOrder {
		team := s.teams[teamID]
		for _, channelID := range team.channelOrder {
			ch := team.channels[channelID]
			if !hasMember(ch.members, s.me) {
				continue
			}
			for _, root := range ch.messages {
				for _, m := range append([]*messageRecord{root}, root.replies...) {
					if s.matchMessage(m, kql) {
						out = append(out, m)
					}
				}
			}
		}
	}
	for _, chatID := range s.chatOrder {
		chat := s.chats[chatID]
		if chat.deleted || !hasMember(chat.members, s.me) {
			continue
		}
		for _, m := range chat.messages {
			if s.matchMessage(m, kql) {
				out = append(out, m)
			}
		}
	}
	return out
}

// matchMessage applies the KQL subset to one message.
func (s *store) matchMessage(m *messageRecord, kql *kqlQuery) bool {
	if !m.deleted.IsZero() {
		return false
	}
	haystack := strings.ToLower(m.body.Content + " " + m.subject)
	for _, term := range kql.freeText {
		if !strings.Contains(haystack, term) {
			return false
		}
	}
	if kql.from != "" {
		u := s.users[m.authorID]
		if u == nil || !identityMatches(u, kql.from) {
			return false
		}
	}
	if kql.to != "" {
		if !s.chatHasMember(m.chatID, kql.to) {
			return false
		}
	}
	if kql.isMentioned != nil && mentionOf(m, s.me) != *kql.isMentioned {
		return false
	}
	for _, id := range kql.mentions {
		if !mentionMatches(m, id) {
			return false
		}
	}
	if kql.hasAttachment != nil && (len(m.attachments) > 0) != *kql.hasAttachment {
		return false
	}
	if !kql.sent.matches(m.created) {
		return false
	}
	return true
}

// chatHasMember reports whether a one-on-one chat's other member matches a
// `to:` term. The docs mark `to` as "partially supported for the one-on-one
// message" (search-concept-chat-messages.md:268).
func (s *store) chatHasMember(chatID, term string) bool {
	chat := s.chats[chatID]
	if chat == nil || chat.chatType != ChatTypeOneOnOne {
		return false
	}
	for _, member := range chat.members {
		if member.userID == s.me {
			continue
		}
		if u := s.users[member.userID]; u != nil && identityMatches(u, term) {
			return true
		}
	}
	return false
}

// identityMatches reports whether a from:/to: term names a user.
func identityMatches(u *userRec, term string) bool {
	term = strings.ToLower(strings.TrimSpace(term))
	if term == "" {
		return false
	}
	if strings.EqualFold(u.id, term) || strings.EqualFold(u.upn, term) || strings.EqualFold(u.mail, term) {
		return true
	}
	return strings.Contains(strings.ToLower(u.displayName), term)
}

// mentionOf reports whether a message mentions a user.
func mentionOf(m *messageRecord, userID string) bool {
	for _, men := range m.mentions {
		if men.Mentioned == nil || men.Mentioned.User == nil {
			continue
		}
		if strings.EqualFold(men.Mentioned.User.ID, userID) {
			return true
		}
	}
	return false
}

// mentionMatches matches the documented `mentions:<userId without dashes>`
// term (search-concept-chat-messages.md:267).
func mentionMatches(m *messageRecord, withoutDashes string) bool {
	for _, men := range m.mentions {
		if men.Mentioned == nil || men.Mentioned.User == nil {
			continue
		}
		if strings.EqualFold(stripDashes(men.Mentioned.User.ID), withoutDashes) {
			return true
		}
	}
	return false
}

func stripDashes(s string) string { return strings.ReplaceAll(s, "-", "") }

// searchResource renders the bodyless chatMessage a hit carries. The spike's
// hit resource keys were "@odata.type, channelIdentity, chatId,
// createdDateTime, etag, from, id, importance, lastModifiedDateTime, subject,
// webLink" — notably no body (docs/spike/phase1.md:83).
func (s *store) searchResource(m *messageRecord) json.RawMessage {
	resource := map[string]any{
		"@odata.type":          "microsoft.graph.chatMessage",
		"id":                   m.id,
		"createdDateTime":      graphTime(m.created),
		"lastModifiedDateTime": graphTime(m.modified),
		"etag":                 s.etag(m),
		"importance":           m.importance,
		"subject":              m.subject,
		"webLink":              s.messageWebURL(m),
	}
	if m.chatID != "" {
		resource["chatId"] = m.chatID
	} else if m.channelID != "" {
		resource["channelIdentity"] = channelIdentity{ChannelID: m.channelID, TeamID: m.teamID}
	}
	author := s.displayAuthor(m.authorID)
	if author != nil && author.User != nil {
		resource["from"] = map[string]any{
			"emailAddress": map[string]any{"name": author.User.DisplayName, "address": s.authorAddress(m.authorID)},
		}
	}
	raw, _ := json.Marshal(resource)
	return raw
}

// authorAddress returns the mail or UPN of a message author, which is what the
// search example's from.emailAddress.address carries
// (refs/graph/concepts/search-concept-chat-messages.md:370-375).
func (s *store) authorAddress(authorID string) string {
	if u := s.users[authorID]; u != nil {
		return firstNonEmpty(u.mail, u.upn)
	}
	return authorID
}

// sentFilter is the parsed `sent>…`/`sent>=…`/`sent<…`/`sent<=…` comparison.
type sentFilter struct {
	set       bool
	op        string
	value     time.Time
	inclusive bool
}

func (f sentFilter) matches(t time.Time) bool {
	if !f.set {
		return true
	}
	switch f.op {
	case ">":
		return t.After(f.value)
	case ">=":
		return t.Equal(f.value) || t.After(f.value)
	case "<":
		return t.Before(f.value)
	case "<=":
		return t.Equal(f.value) || t.Before(f.value)
	default:
		return true
	}
}

// kqlQuery is the KQL subset the fake understands.
type kqlQuery struct {
	freeText      []string
	from          string
	to            string
	mentions      []string
	isMentioned   *bool
	hasAttachment *bool
	sent          sentFilter
}

// parseKQL parses the supported scope terms plus free text. An unsupported
// scope term is a 400 rather than a silently ignored term, because silently
// ignoring a query term is the bug class the CLI is built to avoid.
func parseKQL(raw string) (*kqlQuery, *apiError) {
	out := &kqlQuery{}
	for _, token := range tokenizeKQL(raw) {
		lower := strings.ToLower(token)
		switch {
		case strings.HasPrefix(lower, "isread:"):
			// Documented but broken in the live service: it returned 500 every
			// time, whatever the casing, alone or combined
			// (docs/spike/phase1.md:79). The fake reproduces the 500 so the CLI
			// cannot accidentally depend on it.
			return nil, &apiError{
				Status:  http.StatusInternalServerError,
				Code:    "InternalServerError",
				Message: "The search request failed.",
			}
		case strings.HasPrefix(lower, "ismentioned:"):
			v, ok := parseBoolTerm(token[len("ismentioned:"):])
			if !ok {
				return nil, badRequestf("IsMentioned expects true or false, got %q.", token)
			}
			out.isMentioned = &v
		case strings.HasPrefix(lower, "hasattachment:"):
			v, ok := parseBoolTerm(token[len("hasattachment:"):])
			if !ok {
				return nil, badRequestf("hasAttachment expects true or false, got %q.", token)
			}
			out.hasAttachment = &v
		case strings.HasPrefix(lower, "mentions:"):
			out.mentions = append(out.mentions, stripDashes(strings.ToLower(token[len("mentions:"):])))
		case strings.HasPrefix(lower, "from:"):
			out.from = token[len("from:"):]
		case strings.HasPrefix(lower, "to:"):
			out.to = token[len("to:"):]
		case strings.HasPrefix(lower, "sent"):
			f, ok := parseSentTerm(token)
			if !ok {
				return nil, badRequestf("The 'sent' term %q is not a valid date comparison.", token)
			}
			out.sent = f
		case strings.Contains(token, ":"):
			return nil, badRequestf("The scope term %q is not supported by this query.", token)
		default:
			out.freeText = append(out.freeText, strings.ToLower(strings.Trim(token, `"`)))
		}
	}
	return out, nil
}

// parseSentTerm parses sent>DATE, sent>=DATE, sent<DATE and sent<=DATE. A bare
// date means the whole day, which is what the KQL reference says for date
// ranges ("from the beginning of day A to the end of day B"); the spike
// confirmed sent>D excludes all of day D. A full timestamp is compared as
// given, because the live service honours the time of day
// (refs/kql/docs/general-development/keyword-query-language-kql-syntax-reference.md:111,186;
// docs/spike/phase1.md:80-81).
func parseSentTerm(token string) (sentFilter, bool) {
	rest := token[len("sent"):]
	var op string
	for _, candidate := range []string{">=", "<=", ">", "<"} {
		if strings.HasPrefix(rest, candidate) {
			op = candidate
			rest = rest[len(candidate):]
			break
		}
	}
	if op == "" {
		return sentFilter{}, false
	}
	rest = strings.Trim(strings.TrimSpace(rest), `"`)
	value, bareDate, ok := parseKQLTime(rest)
	if !ok {
		return sentFilter{}, false
	}
	f := sentFilter{set: true, op: op, value: value}
	if bareDate {
		// A bare date is interpreted in UTC, the timezone the KQL reference
		// mandates; the spike could not confirm the boundary, so this choice is
		// documented rather than observed (docs/spike/phase1.md:82).
		day := value.AddDate(0, 0, 1)
		switch op {
		case ">":
			f.value = day
		case ">=":
			f.value = value
		case "<":
			f.value = value
		case "<=":
			f.value = day.Add(-time.Nanosecond)
		}
	}
	return f, true
}

// parseKQLTime parses the ISO 8601 forms the KQL reference lists, reporting
// whether the input was a bare date.
func parseKQLTime(raw string) (time.Time, bool, bool) {
	if len(raw) == len("2006-01-02") {
		if t, err := time.Parse("2006-01-02", raw); err == nil {
			return t.UTC(), true, true
		}
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02T15:04:05", "2006-01-02T15:04"} {
		if t, err := time.Parse(layout, raw); err == nil {
			return t.UTC(), false, true
		}
	}
	return time.Time{}, false, false
}

func parseBoolTerm(raw string) (bool, bool) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "true":
		return true, true
	case "false":
		return false, true
	default:
		return false, false
	}
}

// tokenizeKQL splits a query on whitespace, keeping quoted phrases together.
func tokenizeKQL(raw string) []string {
	var out []string
	var b strings.Builder
	inQuote := false
	flush := func() {
		if b.Len() > 0 {
			out = append(out, b.String())
			b.Reset()
		}
	}
	for _, r := range raw {
		switch {
		case r == '"':
			inQuote = !inQuote
			b.WriteRune(r)
		case (r == ' ' || r == '\t' || r == '\n') && !inQuote:
			flush()
		default:
			b.WriteRune(r)
		}
	}
	flush()
	return out
}
