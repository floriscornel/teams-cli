package graph

import (
	"context"
	"encoding/json"
	"strings"
	"time"
)

// This file wraps POST /search/query, the endpoint behind "teams search" and
// "teams mentions".
//
// The response shape is searchResponse: hits arrive inside hitsContainers, and
// a hit carries a bodyless chatMessage (docs/spike/phase1.md:83). Paging is
// from/size rather than @odata.nextLink, and the first page must start at zero
// (refs/graph/api-reference/v1.0/resources/search-api-overview.md:67).

// SearchQuery is one search request.
type SearchQuery struct {
	// Query is the KQL query string, built with SearchFilters.KQL or typed by
	// the user.
	Query string
	// From is the 0-based offset; the first page must use 0.
	From int
	// Size is the page size, clamped to the observed 50 for chatMessage.
	Size int
	// Fields narrows the returned properties. chatMessage search ignores it.
	Fields []string
}

// SearchHit is one searchHit (refs/graph/api-reference/v1.0/resources/searchhit.md).
type SearchHit struct {
	HitID    string         `json:"hitId"`
	Rank     int            `json:"rank"`
	Summary  string         `json:"summary,omitempty"`
	Resource SearchResource `json:"resource"`
}

// SearchResource is the bodyless chatMessage a hit carries. Graph sends
// webLink rather than webUrl here
// (refs/graph/concepts/search-concept-chat-messages.md:365-380).
type SearchResource struct {
	ODataType            string           `json:"@odata.type,omitempty"`
	ID                   string           `json:"id,omitempty"`
	ChatID               string           `json:"chatId,omitempty"`
	ChannelIdentity      *ChannelIdentity `json:"channelIdentity,omitempty"`
	CreatedDateTime      time.Time        `json:"createdDateTime,omitzero"`
	LastModifiedDateTime time.Time        `json:"lastModifiedDateTime,omitzero"`
	Subject              string           `json:"subject,omitempty"`
	Importance           string           `json:"importance,omitempty"`
	ETag                 string           `json:"etag,omitempty"`
	WebLink              string           `json:"webLink,omitempty"`
	From                 *SearchSender    `json:"from,omitempty"`
}

// SearchSender is the search hit's "from", which is an emailAddress pair rather
// than the chatMessage identitySet.
type SearchSender struct {
	EmailAddress struct {
		Name    string `json:"name,omitempty"`
		Address string `json:"address,omitempty"`
	} `json:"emailAddress"`
}

// DisplayName is the sender's name, falling back to the address.
func (s *SearchSender) DisplayName() string {
	if s == nil {
		return ""
	}
	if s.EmailAddress.Name != "" {
		return s.EmailAddress.Name
	}
	return s.EmailAddress.Address
}

// InChannel reports whether the hit is a channel message and returns its
// channel and team ids.
func (r SearchResource) InChannel() (teamID, channelID string, ok bool) {
	if r.ChannelIdentity == nil {
		return "", "", false
	}
	return r.ChannelIdentity.TeamID, r.ChannelIdentity.ChannelID, true
}

// SearchResult is the useful part of a search response: the hits of the single
// hitsContainer the API returns, plus the counts.
type SearchResult struct {
	SearchTerms          []string    `json:"searchTerms,omitempty"`
	Hits                 []SearchHit `json:"hits"`
	Total                int         `json:"total"`
	MoreResultsAvailable bool        `json:"moreResultsAvailable"`
}

// searchEnvelope is the request body: the API accepts a collection of
// searchRequests but "currently supports only a single searchRequest at a time"
// (refs/graph/api-reference/v1.0/resources/search-api-overview.md:182).
type searchEnvelope struct {
	Requests []searchRequest `json:"requests"`
}

type searchRequest struct {
	EntityTypes []string `json:"entityTypes"`
	Query       struct {
		QueryString string `json:"queryString"`
	} `json:"query"`
	From   *int     `json:"from,omitempty"`
	Size   *int     `json:"size,omitempty"`
	Fields []string `json:"fields,omitempty"`
}

// searchResultWire is the {"value":[...]} envelope.
type searchResultWire struct {
	Value []searchResponseWire `json:"value"`
}

type searchResponseWire struct {
	SearchTerms    []string              `json:"searchTerms"`
	HitsContainers []searchHitsContainer `json:"hitsContainers"`
}

type searchHitsContainer struct {
	Hits                 []searchHitWire `json:"hits"`
	Total                int             `json:"total"`
	MoreResultsAvailable bool            `json:"moreResultsAvailable"`
}

type searchHitWire struct {
	HitID    string          `json:"hitId"`
	Rank     int             `json:"rank"`
	Summary  string          `json:"summary,omitempty"`
	Resource json.RawMessage `json:"resource"`
}

// SearchMessages runs one chatMessage search. An empty query string is a usage
// error here rather than a round trip that Graph would reject with 400.
func (c *Client) SearchMessages(ctx context.Context, q SearchQuery) (SearchResult, error) {
	if strings.TrimSpace(q.Query) == "" {
		return SearchResult{}, usageError("graph: search needs a query string")
	}
	if q.From < 0 {
		return SearchResult{}, usageError("graph: search offset must not be negative")
	}
	size := topValue(q.Size, DefaultTopSearch, MaxTopSearch)
	from := q.From
	req := searchEnvelope{Requests: []searchRequest{{
		// chatMessage cannot be mixed with another entity type, and its
		// permission set is Chat.Read / Chat.ReadWrite / ChannelMessage.Read.All
		// (refs/INDEX.md, "the search note").
		EntityTypes: []string{"chatMessage"},
		From:        &from,
		Size:        &size,
		Fields:      q.Fields,
	}}}
	req.Requests[0].Query.QueryString = q.Query

	var wire searchResultWire
	if err := c.Post(ctx, "/search/query", req, &wire); err != nil {
		return SearchResult{}, err
	}
	out := SearchResult{}
	if len(wire.Value) == 0 {
		return out, nil
	}
	resp := wire.Value[0]
	out.SearchTerms = resp.SearchTerms
	if len(resp.HitsContainers) == 0 {
		return out, nil
	}
	container := resp.HitsContainers[0]
	out.Total = container.Total
	out.MoreResultsAvailable = container.MoreResultsAvailable
	out.Hits = make([]SearchHit, 0, len(container.Hits))
	for _, h := range container.Hits {
		hit := SearchHit{HitID: h.HitID, Rank: h.Rank, Summary: h.Summary}
		raw := strings.TrimSpace(string(h.Resource))
		if raw != "" && raw != "null" {
			if err := json.Unmarshal(h.Resource, &hit.Resource); err != nil {
				// A hit whose resource we cannot parse is still a hit: keep the
				// id and the summary rather than failing the whole search.
				hit.Resource = SearchResource{ID: h.HitID}
			}
		}
		out.Hits = append(out.Hits, hit)
	}
	return out, nil
}

// SearchPage fetches one page, which is how the CLI pages a search: the API
// has no next link, so the offset is explicit.
func (c *Client) SearchPage(ctx context.Context, query string, from, size int) (SearchResult, error) {
	return c.SearchMessages(ctx, SearchQuery{Query: query, From: from, Size: size})
}
