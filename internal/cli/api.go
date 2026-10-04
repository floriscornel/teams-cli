package cli

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/floriscornel/teams-cli/internal/graph"
	"github.com/floriscornel/teams-cli/internal/output"
)

// `teams api` is the raw Graph escape hatch, the counterpart of `gh api`: it
// sends one request and prints the response, so anything this CLI does not wrap
// is still reachable.
//
// Three things it deliberately does not do: it does not pre-check scopes (the
// command is defined by the caller's own request, so the only honest answer is
// Graph's), it does not retry a non-idempotent verb differently from any other
// call (the client's retry rules are the documented 429 ones), and it does not
// reach another host — a path is resolved against the configured cloud.

func (a *App) newAPICmd() *cobra.Command {
	var (
		fields  []string
		typed   []string
		input   string
		headers []string
		query   []string
		dryRun  bool
	)
	cmd := &cobra.Command{
		Use:   "api <method> <path>",
		Short: "Call Microsoft Graph directly",
		Long: "Call a Graph endpoint directly, for the operations the CLI does not wrap.\n\n" +
			"<method> is an HTTP method (GET, POST, PATCH, PUT, DELETE) and <path> is a\n" +
			"Graph path such as /me or /teams/{id}/channels; it may carry a query string.\n\n" +
			"Fields:\n" +
			"  -f key=value   a string field, dotted keys nest (body.content=hi)\n" +
			"  -F key=value   a typed field: true, false, null and numbers keep their type\n" +
			"  --input <f>    the whole JSON body from a file, or - for stdin\n\n" +
			"Read-only mode refuses every method but GET and HEAD.",
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			method := strings.ToUpper(strings.TrimSpace(args[0]))
			switch method {
			case http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPatch, http.MethodPut, http.MethodDelete:
			default:
				return output.Usagef("%q is not a supported HTTP method", args[0])
			}
			path := strings.TrimSpace(args[1])
			if path == "" {
				return output.Usagef("the path is empty")
			}

			body, err := apiBody(fields, typed, input, a.Stdin)
			if err != nil {
				return err
			}
			header := http.Header{}
			for _, h := range headers {
				name, value, ok := strings.Cut(h, ":")
				if !ok || strings.TrimSpace(name) == "" {
					return output.Usagef("--header %q: use \"Name: value\"", h)
				}
				header.Set(strings.TrimSpace(name), strings.TrimSpace(value))
			}
			values := url.Values{}
			for _, q := range query {
				name, value, ok := strings.Cut(q, "=")
				if !ok || strings.TrimSpace(name) == "" {
					return output.Usagef("--query %q: use key=value", q)
				}
				values.Add(strings.TrimSpace(name), value)
			}

			if dryRun {
				var preview any
				if len(body) > 0 {
					preview = json.RawMessage(body)
				}
				return a.printDryRun(dryRunDocument{
					Method: method, Path: path, Body: preview,
					Paths: headerLines(header),
				})
			}

			client, err := a.Graph(ctx)
			if err != nil {
				return err
			}
			a.Printer.Statusf("%s %s", method, path)
			resp, err := client.Do(ctx, graph.Request{
				Method: method,
				Path:   path,
				Query:  values,
				Raw:    body,
				Header: header,
			})
			if err != nil {
				return err
			}
			if resp.StatusCode == http.StatusNoContent || method == http.MethodHead {
				return nil
			}
			if isJSONResponse(resp) {
				return a.Printer.JSONBytes(resp.Body)
			}
			if _, err := a.Printer.Out().Write(resp.Body); err != nil {
				return output.Errorf("write the response: %v", err)
			}
			return nil
		},
	}
	cmd.Flags().StringArrayVarP(&fields, "field", "f", nil, "add a string field to the JSON body (key=value, dotted keys nest)")
	cmd.Flags().StringArrayVarP(&typed, "raw-field", "F", nil, "add a typed field (true/false, a number and null keep their type)")
	cmd.Flags().StringVar(&input, "input", "", "read the JSON body from a file, or - for stdin")
	cmd.Flags().StringArrayVar(&headers, "header", nil, "extra request header (\"Name: value\", repeatable)")
	cmd.Flags().StringArrayVar(&query, "query", nil, "query parameter (key=value, repeatable)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "print the request without sending it")
	return cmd
}

// apiBody assembles the request body from -f/-F/--input.
//
// A dotted key nests, which is what a caller needs for the one property Graph
// wraps ("body.content=…"); --input is the whole body and cannot be combined
// with the field flags, because there would be no defined merge.
func apiBody(fields, typed []string, input string, stdin io.Reader) ([]byte, error) {
	if input != "" && (len(fields) > 0 || len(typed) > 0) {
		return nil, output.Usagef("--input is the whole body; do not combine it with -f or -F")
	}
	if input != "" {
		if input == "-" {
			data, err := io.ReadAll(stdin)
			if err != nil {
				return nil, output.Errorf("read the body from stdin: %v", err)
			}
			return validateJSONBody(data)
		}
		//nolint:gosec // the caller names the file on purpose: this is `teams api --input <file>`
		data, err := os.ReadFile(input)
		if err != nil {
			return nil, output.Errorf("read %s: %v", input, err)
		}
		return validateJSONBody(data)
	}
	if len(fields) == 0 && len(typed) == 0 {
		return nil, nil
	}
	root := map[string]any{}
	for _, field := range fields {
		key, value, ok := strings.Cut(field, "=")
		if !ok || strings.TrimSpace(key) == "" {
			return nil, output.Usagef("-f %q: use key=value", field)
		}
		if err := nestField(root, strings.TrimSpace(key), value); err != nil {
			return nil, err
		}
	}
	for _, field := range typed {
		key, value, ok := strings.Cut(field, "=")
		if !ok || strings.TrimSpace(key) == "" {
			return nil, output.Usagef("-F %q: use key=value", field)
		}
		if err := nestField(root, strings.TrimSpace(key), typedValue(value)); err != nil {
			return nil, err
		}
	}
	out, err := json.Marshal(root)
	if err != nil {
		return nil, output.Errorf("encode the request body: %v", err)
	}
	return out, nil
}

// nestField sets key in root, creating the intermediate objects a dotted key
// names.
func nestField(root map[string]any, key string, value any) error {
	parts := strings.Split(key, ".")
	node := root
	for _, part := range parts[:len(parts)-1] {
		next, ok := node[part]
		if !ok {
			child := map[string]any{}
			node[part] = child
			node = child
			continue
		}
		child, ok := next.(map[string]any)
		if !ok {
			return output.Usagef("%q is already a plain value, so it cannot hold %q", part, key)
		}
		node = child
	}
	last := parts[len(parts)-1]
	if last == "" {
		return output.Usagef("%q does not name a field", key)
	}
	node[last] = value
	return nil
}

// typedValue interprets a -F value: the JSON literals keep their type, and
// everything else stays a string.
func typedValue(value string) any {
	switch value {
	case "true":
		return true
	case "false":
		return false
	case "null":
		return nil
	}
	if n, err := strconv.ParseInt(value, 10, 64); err == nil {
		return n
	}
	if f, err := strconv.ParseFloat(value, 64); err == nil {
		return f
	}
	return value
}

// validateJSONBody rejects a body that is not JSON, before it is sent: Graph
// would answer 400, and a local error says which file or stream was wrong.
func validateJSONBody(data []byte) ([]byte, error) {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 {
		return nil, nil
	}
	if !json.Valid(trimmed) {
		return nil, output.Usagef("the request body is not valid JSON")
	}
	return trimmed, nil
}

// isJSONResponse reports whether a response body should go through the JSON
// printer. Graph labels its JSON responses application/json; some batch and
// error shapes come back as text/plain with a JSON body, so a leading brace is
// accepted too.
func isJSONResponse(resp *graph.Response) bool {
	if strings.Contains(resp.Header.Get("Content-Type"), "json") {
		return true
	}
	trimmed := bytes.TrimSpace(resp.Body)
	return len(trimmed) > 0 && (trimmed[0] == '{' || trimmed[0] == '[')
}

// headerLines renders the headers a --dry-run document reports.
func headerLines(header http.Header) []string {
	if len(header) == 0 {
		return nil
	}
	names := make([]string, 0, len(header))
	for name := range header {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]string, 0, len(names))
	for _, name := range names {
		out = append(out, name+": "+header.Get(name))
	}
	return out
}
