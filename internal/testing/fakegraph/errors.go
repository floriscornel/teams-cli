package fakegraph

import "fmt"

// apiError is a Graph-shaped failure a handler can report with c.fail.
type apiError struct {
	Status  int
	Code    string
	Message string
}

// Error implements error.
func (e *apiError) Error() string {
	return fmt.Sprintf("fakegraph: HTTP %d %s: %s", e.Status, e.Code, e.Message)
}

// badRequestf builds the 400 Graph returns for a query parameter it refuses.
// The wording follows the live messages the spike saw: an unsupported parameter
// answers "Parameter 'Filter' not supported" and an over-large page size names
// the limit (docs/spike/phase1.md:52-53).
func badRequestf(format string, args ...any) *apiError {
	return &apiError{Status: 400, Code: "BadRequest", Message: fmt.Sprintf(format, args...)}
}

// notFoundf builds a 404.
func notFoundf(format string, args ...any) *apiError {
	return &apiError{Status: 404, Code: "itemNotFound", Message: fmt.Sprintf(format, args...)}
}

// forbiddenf builds a 403.
func forbiddenf(code, format string, args ...any) *apiError {
	if code == "" {
		code = "Authorization_RequestDenied"
	}
	return &apiError{Status: 403, Code: code, Message: fmt.Sprintf(format, args...)}
}

// conflictf builds a 409.
func conflictf(format string, args ...any) *apiError {
	return &apiError{Status: 409, Code: "Conflict", Message: fmt.Sprintf(format, args...)}
}
