package fakegraph

import (
	"net/url"
	"sort"
	"strings"
	"time"
)

// This file implements the chat message list's query matrix. It is the part of
// Graph's Teams surface that deviates most from the generic OData rules, so it
// is isolated and table-tested.
//
// The documented rules (refs/graph/api-reference/v1.0/api/chat-list-messages.md:32-34):
//
//   - `$orderby` supports lastModifiedDateTime (the default) and
//     createdDateTime, descending only; ascending is not supported;
//   - `$filter` supports gt and lt on lastModifiedDateTime and lt on
//     createdDateTime;
//   - the filter is ignored unless the request also has an `$orderby` for the
//     same property.
//
// The spike corrected one of them and added one observation (docs/spike/phase1.md:60-65):
//
//   - `lastModifiedDateTime gt` worked even without `$orderby`, so the fake
//     applies it either way;
//   - `createdDateTime lt` without `$orderby` was ignored, which matches the
//     docs;
//   - the default listing is sorted by neither date property.

// dateProperty names the two properties a chat message query may use.
const (
	propLastModified = "lastModifiedDateTime"
	propCreated      = "createdDateTime"
)

// chatMessageQuery is the parsed query.
type chatMessageQuery struct {
	// OrderBy is the property to sort descending by, or "" when the request
	// did not ask for an order (the default, deliberately unsorted case).
	OrderBy string
	// FilterProp, FilterOp and FilterValue describe the applied filter. The
	// filter is dropped (not applied) when the docs say it is ignored.
	FilterProp  string
	FilterOp    string
	FilterValue time.Time
	// IgnoredFilter records a filter the request sent but that the server
	// ignores, so a test can assert on it.
	IgnoredFilter bool
}

// parseChatMessageQuery validates `$orderby` and `$filter` and decides whether
// the filter applies.
func parseChatMessageQuery(q url.Values) (chatMessageQuery, *apiError) {
	var out chatMessageQuery
	rawOrder := strings.TrimSpace(q.Get("$orderby"))
	if rawOrder != "" {
		prop, dir, err := parseOrderBy(rawOrder)
		if err != nil {
			return out, err
		}
		if dir != "" && !strings.EqualFold(dir, "desc") {
			// The api-reference is explicit: "The ascending order is currently
			// not supported" (chat-list-messages.md:33); the spike got 400.
			return out, badRequestf("The '$orderby' value '%s' is not supported. Only descending order is supported.", rawOrder)
		}
		out.OrderBy = prop
	}

	rawFilter := strings.TrimSpace(q.Get("$filter"))
	if rawFilter == "" {
		return out, nil
	}
	prop, op, rawValue, err := parseDateFilter(rawFilter)
	if err != nil {
		return out, err
	}
	value, terr := parseODataTime(rawValue)
	if terr != nil {
		return out, badRequestf("The '$filter' value '%s' is not a valid dateTimeOffset.", rawFilter)
	}
	switch prop {
	case propCreated:
		if op != "lt" {
			// createdDateTime supports only lt
			// (refs/graph/api-reference/v1.0/api/chat-list-messages.md:34).
			return out, badRequestf("The '$filter' value '%s' is not supported. 'createdDateTime' supports only the 'lt' operator.", rawFilter)
		}
		if out.OrderBy != propCreated {
			// Ignored without a matching $orderby — the docs say so and the
			// spike confirmed it for createdDateTime lt
			// (docs/spike/phase1.md:63).
			out.IgnoredFilter = true
			return out, nil
		}
	case propLastModified:
		// The docs say the filter needs a matching $orderby, but the spike saw
		// lastModifiedDateTime gt applied without one (docs/spike/phase1.md:62),
		// so the fake accepts both. It is still ignored when $orderby names the
		// other property.
		if out.OrderBy != "" && out.OrderBy != propLastModified {
			out.IgnoredFilter = true
			return out, nil
		}
	default:
		return out, badRequestf("The '$filter' value '%s' is not supported. Use 'lastModifiedDateTime' or 'createdDateTime'.", rawFilter)
	}
	out.FilterProp = prop
	out.FilterOp = op
	out.FilterValue = value
	return out, nil
}

// parseOrderBy splits "$orderby=prop desc".
func parseOrderBy(raw string) (prop, dir string, err *apiError) {
	fields := strings.Fields(raw)
	if len(fields) == 0 || len(fields) > 2 {
		return "", "", badRequestf("The '$orderby' value '%s' is not supported.", raw)
	}
	prop = fields[0]
	if prop != propLastModified && prop != propCreated {
		return "", "", badRequestf("The '$orderby' value '%s' is not supported. Use 'lastModifiedDateTime' or 'createdDateTime'.", raw)
	}
	if len(fields) == 2 {
		dir = fields[1]
	}
	return prop, dir, nil
}

// parseDateFilter splits a filter expression. Only the single-comparison form
// the api-reference documents is supported.
func parseDateFilter(raw string) (prop, op, value string, err *apiError) {
	fields := strings.Fields(raw)
	if len(fields) != 3 {
		return "", "", "", badRequestf("The '$filter' value '%s' is not supported. Use a single comparison such as 'lastModifiedDateTime gt 2024-01-01T00:00:00Z'.", raw)
	}
	prop, op, value = fields[0], strings.ToLower(fields[1]), fields[2]
	if op != "gt" && op != "lt" {
		return "", "", "", badRequestf("The '$filter' value '%s' is not supported. Only 'gt' and 'lt' are.", raw)
	}
	return prop, op, value, nil
}

// parseODataTime parses the unquoted ISO 8601 form OData uses, tolerating a
// surrounding quote pair and a fractional-second part.
func parseODataTime(raw string) (time.Time, error) {
	raw = strings.Trim(raw, "'\"")
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02T15:04:05", "2006-01-02"} {
		if t, err := time.Parse(layout, raw); err == nil {
			return t.UTC(), nil
		}
	}
	return time.Time{}, errNotImplemented
}

// apply filters and orders a chat's messages.
func (q chatMessageQuery) apply(msgs []*messageRecord) []*messageRecord {
	out := make([]*messageRecord, 0, len(msgs))
	for _, m := range msgs {
		if !m.deleted.IsZero() {
			continue
		}
		if q.FilterProp != "" {
			value := m.modified
			if q.FilterProp == propCreated {
				value = m.created
			}
			switch q.FilterOp {
			case "gt":
				if !value.After(q.FilterValue) {
					continue
				}
			case "lt":
				if !value.Before(q.FilterValue) {
					continue
				}
			}
		}
		out = append(out, m)
	}
	switch q.OrderBy {
	case propCreated:
		sortMessagesDesc(out, func(m *messageRecord) time.Time { return m.created })
	case propLastModified:
		sortMessagesDesc(out, func(m *messageRecord) time.Time { return m.modified })
	default:
		// No $orderby: the live service returns neither created- nor
		// modified-ordered messages, so callers must sort client-side
		// (docs/spike/phase1.md:60). The fake scrambles deterministically.
		return hashOrder(out)
	}
	return out
}

func sortMessagesDesc(msgs []*messageRecord, key func(*messageRecord) time.Time) {
	sort.SliceStable(msgs, func(i, j int) bool {
		ki, kj := key(msgs[i]), key(msgs[j])
		if !ki.Equal(kj) {
			return ki.After(kj)
		}
		return msgs[i].id > msgs[j].id
	})
}
