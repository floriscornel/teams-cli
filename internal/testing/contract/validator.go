package contract

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/getkin/kin-openapi/openapi3filter"
	"github.com/getkin/kin-openapi/routers"
	"github.com/getkin/kin-openapi/routers/gorillamux"

	"github.com/floriscornel/teams-cli/internal/testing/testdata"
)

// The committed artifacts are embedded through internal/testing/testdata, so
// `go test ./...` needs neither a refs/ checkout nor network access
// (PLAN.md: "Tests never need refs/").
var (
	trimmedSpecYAML []byte
	routesTXT       []byte
)

func init() {
	var err error
	trimmedSpecYAML, err = testdata.OpenAPI.ReadFile(embeddedSpecPath)
	if err != nil {
		panic("contract: embedded trimmed spec missing: " + err.Error())
	}
	routesTXT, err = testdata.OpenAPI.ReadFile(embeddedRoutesPath)
	if err != nil {
		panic("contract: embedded route list missing: " + err.Error())
	}
}

// Paths of the embedded artifacts, relative to internal/testing/testdata.
const (
	embeddedSpecPath   = "openapi/graph-v1.0-trimmed.yaml"
	embeddedRoutesPath = "openapi/routes.txt"
)

// ErrBatchExempt is returned by ValidateRequest and ValidateResponse for
// /$batch. The Graph OpenAPI description contains no /$batch path and no batch
// schema at all (PLAN.md Layer 6), so a batch envelope cannot be checked against
// it; ValidateBatchEnvelope checks it against a small schema of ours instead.
var ErrBatchExempt = errors.New("contract: exempt: $batch has no path or schema in the trimmed Graph OpenAPI description; use ValidateBatchEnvelope")

// BatchPath is the Graph JSON-batching endpoint, which is exempt from
// spec-based validation (PLAN.md Layer 6).
const BatchPath = "/$batch"

// batchEnvelopeSchema is OUR schema, not Microsoft's. The description ships no
// batch schema, so this is derived from the api-reference and the concepts page:
//
//   - refs/graph/ has no batch endpoint page: refs/INDEX.md ("Inline images have
//     no create endpoint...") and PLAN.md Layer 6 both record that the
//     description contains no /$batch path and no batch schema;
//   - refs/graph/concepts/json-batching.md documents the envelope: a "requests"
//     array of at most 20 objects, each with an "id", a "method" and a "url",
//     plus optional "headers" and "body".
//
// ValidateBatchEnvelope implements it by hand rather than through a JSON Schema
// library; batchEnvelopeSchema is kept as the written-down contract that the
// hand-written checks and its test both refer to.
const batchEnvelopeSchema = `{
  "type": "object",
  "required": ["requests"],
  "additionalProperties": false,
  "properties": {
    "requests": {
      "type": "array",
      "minItems": 1,
      "maxItems": 20,
      "items": {
        "type": "object",
        "required": ["id", "method", "url"],
        "additionalProperties": false,
        "properties": {
          "id": {"type": "string", "minLength": 1},
          "method": {"type": "string", "enum": ["GET", "POST", "PUT", "PATCH", "DELETE"]},
          "url": {"type": "string", "pattern": "^/[^/]"},
          "headers": {"type": "object", "additionalProperties": {"type": "string"}},
          "body": {}
        }
      }
    }
  }
}`

// Validator checks requests and responses against the trimmed committed spec.
type Validator struct {
	doc    *openapi3.T
	router routers.Router
}

// Load builds a validator from the embedded trimmed spec and route list. It
// needs no file I/O at test time and no refs/ checkout.
//
// The document is fully validated, so a hand-edit that breaks the trimmed copy
// fails the first contract test rather than producing a confusing 404 from the
// router.
//
// The validator is built once per process and shared: loading means parsing and
// schema-compiling a 1 MB description, which costs ~0.4 s normally and ~4 s
// under the race detector, and every contract test used to pay it again. A
// Validator is read-only after construction (the document and the router are
// both final), so sharing it across tests — sequential or parallel — is safe.
var loadValidator = sync.OnceValues(func() (*Validator, error) {
	return newValidator(trimmedSpecYAML, routesTXT)
})

// Load returns the shared validator, failing the calling test if the embedded
// spec cannot be loaded.
func Load(t testing.TB) *Validator {
	t.Helper()
	v, err := loadValidator()
	if err != nil {
		t.Fatalf("contract: load embedded spec: %v", err)
	}
	return v
}

func newValidator(spec, routes []byte) (*Validator, error) {
	loader := openapi3.NewLoader()
	loader.IsExternalRefsAllowed = false
	doc, err := loader.LoadFromData(spec)
	if err != nil {
		return nil, fmt.Errorf("parse embedded spec: %w", err)
	}
	doc.InternalizeRefs(context.Background(), nil)
	if err := doc.Validate(context.Background()); err != nil {
		return nil, fmt.Errorf("validate embedded spec: %w", err)
	}
	router, err := gorillamux.NewRouter(doc)
	if err != nil {
		return nil, fmt.Errorf("build router: %w", err)
	}
	if parsed, err := parseRoutesText(string(routes)); err != nil {
		return nil, err
	} else if len(parsed) == 0 {
		return nil, errors.New("route list is empty")
	}
	return &Validator{doc: doc, router: router}, nil
}

// ValidateRequest checks a request line against the spec: method, path resolved
// against the path templates, query parameters, and a JSON body when one is
// given.
//
// The security scheme is disabled (we do not model our bearer token) and query
// parameters are checked; bodies are only parsed when the operation declares an
// application/json request body, so a raw upload is not misread as JSON.
func (v *Validator) ValidateRequest(method, path string, query url.Values, body []byte) error {
	if isBatchPath(path) {
		return ErrBatchExempt
	}
	route, pathParams, err := v.findRoute(method, path)
	if err != nil {
		return err
	}
	req, err := newRequest(method, path, query, body, route)
	if err != nil {
		return err
	}
	input := &openapi3filter.RequestValidationInput{
		Request:     req,
		PathParams:  pathParams,
		QueryParams: query,
		Route:       route,
		Options:     requestOptions(),
	}
	if err := openapi3filter.ValidateRequest(context.Background(), input); err != nil {
		return wrapRequestError(method, path, query, body, err)
	}
	return nil
}

// ValidateResponse checks a status code and JSON body against the spec for that
// operation. Response body validation is enabled where the spec provides a
// schema for the status code, and response status is included so an
// undocumented status is reported rather than silently accepted.
func (v *Validator) ValidateResponse(method, path string, status int, body []byte) error {
	if isBatchPath(path) {
		return ErrBatchExempt
	}
	route, pathParams, err := v.findRoute(method, path)
	if err != nil {
		return err
	}
	req, err := newRequest(method, path, nil, nil, route)
	if err != nil {
		return err
	}
	input := &openapi3filter.ResponseValidationInput{
		RequestValidationInput: &openapi3filter.RequestValidationInput{
			Request:    req,
			PathParams: pathParams,
			Route:      route,
			Options:    requestOptions(),
		},
		Status:  status,
		Header:  http.Header{"Content-Type": []string{"application/json"}},
		Options: responseOptions(),
	}
	input.SetBodyBytes(body)
	if err := openapi3filter.ValidateResponse(context.Background(), input); err != nil {
		return wrapResponseError(method, path, status, body, err)
	}
	return nil
}

// findRoute resolves a concrete request path to a spec operation.
func (v *Validator) findRoute(method, path string) (*routers.Route, map[string]string, error) {
	// A context is required by the noctx linter; nothing here is network I/O,
	// the request is only a structured path for the router.
	req, err := http.NewRequestWithContext(context.Background(), method, "https://graph.microsoft.com/v1.0"+path, nil)
	if err != nil {
		return nil, nil, &Error{
			Method: method, Path: path, Part: PartPath,
			Value: path,
			Err:   fmt.Errorf("build request: %w", err),
		}
	}
	route, pathParams, err := v.router.FindRoute(req)
	if err != nil {
		part := PartPath
		reason := "does not match any path template in the trimmed spec"
		if errors.Is(err, routers.ErrMethodNotAllowed) {
			reason = "matches a path template but not with this HTTP method"
		}
		return nil, nil, &Error{
			Method: method, Path: path, Part: part,
			Value: method + " " + path,
			Err:   errors.New(reason),
		}
	}
	return route, pathParams, nil
}

// requestOptions disables security (we never model the bearer token) and keeps
// body and query validation on.
func requestOptions() *openapi3filter.Options {
	return &openapi3filter.Options{
		AuthenticationFunc:                openapi3filter.NoopAuthenticationFunc,
		MultiError:                        true,
		ExcludeRequestBody:                false,
		ExcludeRequestQueryParams:         false,
		ExcludeReadOnlyValidations:        false,
		SkipSettingDefaults:               true,
		RejectWhenRequestBodyNotSpecified: false,
	}
}

// responseOptions keeps response-body validation on and reports undocumented
// status codes.
func responseOptions() *openapi3filter.Options {
	return &openapi3filter.Options{
		AuthenticationFunc:          openapi3filter.NoopAuthenticationFunc,
		MultiError:                  true,
		ExcludeResponseBody:         false,
		IncludeResponseStatus:       true,
		SkipSettingDefaults:         true,
		ExcludeWriteOnlyValidations: false,
	}
}

// newRequest builds the http.Request the filter validates. The content type is
// taken from the operation so that a body on an operation whose spec content
// type is not application/json is still validated against the declared type.
func newRequest(method, path string, query url.Values, body []byte, route *routers.Route) (*http.Request, error) {
	target := "https://graph.microsoft.com/v1.0" + path
	if len(query) > 0 {
		target += "?" + query.Encode()
	}
	var reader *bytes.Reader
	if len(body) == 0 {
		reader = bytes.NewReader(nil)
	} else {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(context.Background(), method, target, reader)
	if err != nil {
		return nil, &Error{Method: method, Path: path, Part: PartPath, Value: target, Err: err}
	}
	if len(body) > 0 {
		ct := jsonContentType(route)
		req.Header.Set("Content-Type", ct)
		req.ContentLength = int64(len(body))
	}
	return req, nil
}

// jsonContentType returns the request content type declared by the operation,
// preferring application/json.
func jsonContentType(route *routers.Route) string {
	if route == nil || route.Operation == nil || route.Operation.RequestBody == nil {
		return "application/json"
	}
	content := route.Operation.RequestBody.Value.Content
	if content.Get("application/json") != nil {
		return "application/json"
	}
	for _, ct := range sortedContentKeys(content) {
		return ct
	}
	return "application/json"
}

// sortedContentKeys returns the declared content types of an operation in a
// deterministic order, so the picked content type does not depend on map
// iteration order.
func sortedContentKeys(content openapi3.Content) []string {
	out := make([]string, 0, len(content))
	for k := range content {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// ValidateBatchEnvelope checks a /$batch request body against the small local
// schema documented on batchEnvelopeSchema. It is ours, not Microsoft's: the
// Graph OpenAPI description has no batch path or batch schema (PLAN.md Layer 6).
//
// The checks below are the hand-written equivalent of batchEnvelopeSchema; the
// constant exists so the contract is written down once, and
// TestBatchEnvelopeSchemaMatchesChecks keeps the two in step.
func ValidateBatchEnvelope(body []byte) error {
	var probe any
	if err := json.Unmarshal(body, &probe); err != nil {
		return &Error{
			Method: http.MethodPost, Path: BatchPath, Part: PartBody,
			Value: truncate(body, 200),
			Err:   fmt.Errorf("not valid JSON: %w", err),
		}
	}
	var reqs struct {
		Requests []struct {
			ID     *string           `json:"id"`
			Method *string           `json:"method"`
			URL    *string           `json:"url"`
			Header map[string]string `json:"headers"`
			Body   any               `json:"body"`
		} `json:"requests"`
		Extra map[string]any `json:"-"`
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&reqs); err != nil {
		return &Error{
			Method: http.MethodPost, Path: BatchPath, Part: PartBody,
			Value: truncate(body, 200),
			Err:   fmt.Errorf("envelope does not match the local batch schema: %w", err),
		}
	}
	if len(reqs.Requests) == 0 {
		return batchFieldError("requests", "must contain at least 1 request")
	}
	if len(reqs.Requests) > 20 {
		return batchFieldError("requests", fmt.Sprintf("must contain at most 20 requests, got %d", len(reqs.Requests)))
	}
	seen := map[string]bool{}
	for i, sub := range reqs.Requests {
		switch {
		case sub.ID == nil || *sub.ID == "":
			return batchFieldError(fmt.Sprintf("requests[%d].id", i), "is required and must not be empty")
		case sub.Method == nil:
			return batchFieldError(fmt.Sprintf("requests[%d].method", i), "is required")
		case sub.URL == nil || !strings.HasPrefix(*sub.URL, "/"):
			return batchFieldError(fmt.Sprintf("requests[%d].url", i), "is required and must be a relative Graph path starting with '/'")
		}
		switch *sub.Method {
		case http.MethodGet, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		default:
			return batchFieldError(fmt.Sprintf("requests[%d].method", i), fmt.Sprintf("%q is not one of GET, POST, PUT, PATCH, DELETE", *sub.Method))
		}
		if seen[*sub.ID] {
			return batchFieldError(fmt.Sprintf("requests[%d].id", i), fmt.Sprintf("duplicate %q", *sub.ID))
		}
		seen[*sub.ID] = true
	}
	return nil
}

func batchFieldError(field, reason string) error {
	return &Error{
		Method: http.MethodPost, Path: BatchPath, Part: PartBody,
		Value: field,
		Err:   errors.New(reason),
	}
}

func isBatchPath(path string) bool {
	if i := strings.Index(path, "?"); i >= 0 {
		path = path[:i]
	}
	return path == BatchPath
}
