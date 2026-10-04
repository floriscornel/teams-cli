package graph

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"net/http"
	"net/url"
)

// PreferUnknownEnumMembers is the Prefer header value that makes evolvable
// enums work: without it Graph collapses new members to `unknownFutureValue`, so
// `systemEventMessage` never appears in a parsed messageType
// (refs/graph/api-reference/v1.0/resources/chatmessage.md:81; confirmed in
// docs/spike/phase1.md:54).
const PreferUnknownEnumMembers = "include-unknown-enum-members"

// MaxPages is a safety valve: a server that keeps returning a next link must not
// hang the CLI forever.
const MaxPages = 1000

// Page is one page of a Graph collection.
type Page[T any] struct {
	Value    []T    `json:"value"`
	NextLink string `json:"@odata.nextLink"`
	// Count is only present on the first page (refs/graph/concepts/paging.md:100).
	Count *int `json:"@odata.count"`
	// ODataContext is the documented @odata.context, kept so callers can log it.
	ODataContext string `json:"@odata.context"`
}

// GetPage fetches a single page. path is a Graph path or an absolute next link.
func GetPage[T any](ctx context.Context, c *Client, path string, q url.Values, opts ...RequestOption) (Page[T], error) {
	var page Page[T]
	req := Request{Method: http.MethodGet, Path: path, Query: q}
	for _, opt := range opts {
		opt(&req)
	}
	resp, err := c.Do(ctx, req)
	if err != nil {
		return page, err
	}
	if err := resp.Decode(&page); err != nil {
		return page, err
	}
	return page, nil
}

// EachPage walks every page, following @odata.nextLink verbatim as documented
// (refs/graph/concepts/paging.md:91). fn returns false to stop early. The next
// link carries its own query parameters, so it is requested without ours.
func EachPage[T any](ctx context.Context, c *Client, path string, q url.Values, fn func(Page[T]) (bool, error), opts ...RequestOption) error {
	if fn == nil {
		return errors.New("graph: EachPage needs a callback")
	}
	seen := make(map[string]bool, 4)
	for page := 0; page < MaxPages; page++ {
		if path == "" {
			return nil
		}
		if seen[path] {
			return fmt.Errorf("graph: paging loop detected at %s", path)
		}
		seen[path] = true

		var query url.Values
		if page == 0 {
			query = q
		}
		result, err := GetPage[T](ctx, c, path, query, opts...)
		if err != nil {
			return err
		}
		next, stop, err := func() (string, bool, error) {
			keepGoing, err := fn(result)
			return result.NextLink, !keepGoing, err
		}()
		if err != nil || stop {
			return err
		}
		path = next
	}
	return fmt.Errorf("graph: stopped after %d pages (set a lower --limit)", MaxPages)
}

// Items returns a lazy iterator over every element of a collection, paging
// underneath. It exists so commands can `for item, err := range graph.Items[Chat](...)`
// and stop early without fetching the rest (PLAN.md "paging.go").
func Items[T any](ctx context.Context, c *Client, path string, q url.Values, opts ...RequestOption) iter.Seq2[T, error] {
	return func(yield func(T, error) bool) {
		err := EachPage(ctx, c, path, q, func(page Page[T]) (bool, error) {
			for _, item := range page.Value {
				if !yield(item, nil) {
					return false, nil
				}
			}
			return true, nil
		}, opts...)
		if err != nil {
			var zero T
			yield(zero, err)
		}
	}
}

// Collection helpers used by the endpoint wrappers.

// WithPreferUnknownEnumMembers adds the documented Prefer header.
func WithPreferUnknownEnumMembers() RequestOption {
	return WithHeader("Prefer", PreferUnknownEnumMembers)
}

// Top returns a $top parameter value.
func Top(n int) url.Values {
	return url.Values{"$top": {fmt.Sprint(n)}}
}

// TopWith merges extra parameters with $top.
func TopWith(n int, extra url.Values) url.Values {
	q := url.Values{}
	for k, vs := range extra {
		q[k] = vs
	}
	q.Set("$top", fmt.Sprint(n))
	return q
}
