package fakegraph

import (
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/floriscornel/teams-cli/internal/testing/contract"
)

// This file is the Layer 6 hook (PLAN.md "Contract tests against Microsoft's
// OpenAPI spec"). The fake owns the requests and responses, so it is the right
// place to hand them to kin-openapi: with the hook installed, every call a test
// makes is validated against the trimmed Graph v1.0 description while the test
// runs, and a mismatch fails that test with the offending path.
//
// The hook is opt-in because it costs a spec lookup per request, and because
// the fakes are also used by tests that do not want the spec's opinion.

// ContractValidator is the validator surface [ContractHook] needs. It is an
// interface so the fake's behaviour does not depend on kin-openapi types and so
// a test can substitute a recording stub.
type ContractValidator interface {
	ValidateRequest(method, path string, query url.Values, body []byte) error
	ValidateResponse(method, path string, status int, body []byte) error
}

// ContractReporter is the testing handle a hook reports through.
type ContractReporter interface {
	Helper()
	Errorf(format string, args ...any)
}

// ContractHook validates traffic against the trimmed OpenAPI description that
// internal/testing/contract embeds.
type ContractHook struct {
	validator ContractValidator
	reporter  ContractReporter
}

// WithContract returns an [Options.Contract] hook backed by the Layer 6
// validator. Install it like this:
//
//	srv := fakegraph.New(t, fakegraph.Options{Contract: fakegraph.WithContract(t)})
//
// Every request the fake serves (including /$batch sub-requests) and every
// response it returns is then checked against the spec, and a failure is
// reported with t.Errorf rather than panicking mid-request.
func WithContract(t testing.TB) *ContractHook {
	if t == nil {
		panic("fakegraph: WithContract requires a non-nil testing.TB")
	}
	return &ContractHook{validator: contract.Load(t), reporter: t}
}

// NewContractHook is the injectable form of [WithContract], for a validator
// that is not the embedded one.
func NewContractHook(v ContractValidator, r ContractReporter) *ContractHook {
	return &ContractHook{validator: v, reporter: r}
}

// validate checks one request/response pair. Exemptions (the spec has no
// /$batch path and no schema for it) are silent; everything else is reported.
func (h *ContractHook) validate(method, path string, query url.Values, reqBody []byte, status int, respBody []byte) {
	h.reporter.Helper()
	if err := h.validator.ValidateRequest(method, path, query, reqBody); err != nil && !isContractExempt(err) {
		h.reporter.Errorf("fakegraph: contract: request %s %s: %v", method, path, err)
	}
	if err := h.validator.ValidateResponse(method, path, status, respBody); err != nil && !isContractExempt(err) {
		h.reporter.Errorf("fakegraph: contract: response %s %s (%d): %v", method, path, status, err)
	}
}

// isContractExempt reports whether an error says the spec simply does not cover
// the operation. /$batch is the documented case: the description has no batch
// path or schema at all (refs/INDEX.md, "Three cautions for the contract
// tests"), and internal/testing/contract exposes ErrBatchExempt for it.
func isContractExempt(err error) bool {
	return errors.Is(err, contract.ErrBatchExempt)
}

// validateContract runs the hook for an in-flight request, unless the path is
// one of the fake's own pre-authenticated URLs (a download URL or an upload
// session URL is served by another host in the live service and is not in the
// api-reference, so there is nothing to validate against).
func (s *Server) validateContract(method, rel string, query url.Values, body []byte, status int, resp []byte) {
	if s.opts.Contract == nil {
		return
	}
	if strings.HasPrefix(rel, "/_") {
		return
	}
	s.opts.Contract.validate(method, rel, query, body, status, resp)
}

// Implements reports whether the fake serves an api-reference route template
// such as "GET /teams/{team-id}/channels/{channel-id}/messages" (the shape
// contract.Routes returns). Callers use it to decide whether a committed route
// is worth exercising or should be skipped with a reason.
func (s *Server) Implements(method, template string) bool {
	want := splitPath(template)
	for i := range s.routes {
		route := &s.routes[i]
		if route.notAllowed || !strings.EqualFold(route.method, method) {
			continue
		}
		got := splitPath(route.pattern)
		if len(got) != len(want) {
			continue
		}
		matches := true
		for j := range got {
			gotPlaceholder := isPlaceholder(got[j])
			wantPlaceholder := isPlaceholder(want[j])
			if gotPlaceholder || wantPlaceholder {
				if gotPlaceholder != wantPlaceholder {
					matches = false
					break
				}
				continue
			}
			if !strings.EqualFold(got[j], want[j]) {
				matches = false
				break
			}
		}
		if matches {
			return true
		}
	}
	return false
}

// Routes returns the fake's real route surface as "METHOD /pattern" lines,
// sorted, excluding the 405 placeholders and the fake-only download and upload
// URLs. It is the counterpart to contract.Routes and lets a test compare the
// two lists.
func (s *Server) Routes() []string {
	out := make([]string, 0, len(s.routes))
	for i := range s.routes {
		route := &s.routes[i]
		if route.notAllowed || strings.HasPrefix(route.pattern, "/_") {
			continue
		}
		out = append(out, route.method+" "+route.pattern)
	}
	sortStrings(out)
	return out
}

func isPlaceholder(segment string) bool {
	return strings.HasPrefix(segment, "{") && strings.HasSuffix(segment, "}")
}

// sortStrings is a tiny insertion sort so the package does not import sort in
// one more file.
func sortStrings(values []string) {
	for i := 1; i < len(values); i++ {
		for j := i; j > 0 && values[j] < values[j-1]; j-- {
			values[j], values[j-1] = values[j-1], values[j]
		}
	}
}

// BatchContract validates a recorded /$batch request body with the local batch
// schema, because the Graph description has no batch path or schema (PLAN.md
// Layer 6). A test calls it with the body the recorder captured.
func BatchContract(body []byte) error { return contract.ValidateBatchEnvelope(body) }

// MethodNotAllowed is the status the fake returns for a path that exists with a
// different method. It is exported so tests can assert the documented 405 cases
// (docs/spike/phase1.md:91,99) without magic numbers.
const MethodNotAllowed = http.StatusMethodNotAllowed
