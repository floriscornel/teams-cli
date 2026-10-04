package fakegraph

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/url"
	"time"
)

// RecordedRequest is one request the fake saw, with the response it produced.
// The recorder exists so tests can assert the exact payloads the CLI sends:
// mention `<at id="N">` tags, `hostedContents[]` with its
// `@microsoft.graph.temporaryId`, and attachment references (PLAN.md Layer 2).
type RecordedRequest struct {
	// Seq is the 1-based arrival order across the server's lifetime.
	Seq int
	// Method is the HTTP method.
	Method string
	// Path is the Graph path relative to the service root, for example
	// "/me/chats" or "/teams/t1/channels/c1/messages".
	Path string
	// RawPath is the path as it arrived, including the version segment.
	RawPath string
	// Query holds the decoded query parameters.
	Query url.Values
	// Header holds the request headers.
	Header http.Header
	// Body is the raw request body.
	Body []byte
	// Sub marks a /$batch sub-request.
	Sub bool
	// Status is the response status code.
	Status int
	// Response is the raw response body.
	Response []byte
}

// DecodeBody unmarshals the request body into v.
func (rr *RecordedRequest) DecodeBody(v any) error { return decodeJSON(rr.Body, v) }

// BodyString returns the request body as a string, for substring assertions on
// mention tags or attachment references.
func (rr *RecordedRequest) BodyString() string { return string(rr.Body) }

func decodeJSON(data []byte, v any) error {
	if len(bytes.TrimSpace(data)) == 0 {
		return nil
	}
	return json.Unmarshal(data, v)
}

// recordStart records the request before routing; the response fields are
// filled in by the caller once the handler has run.
func (s *Server) recordStart(r *http.Request, rel string, body []byte, sub bool) *RecordedRequest {
	rec := &RecordedRequest{
		Method:  r.Method,
		Path:    rel,
		RawPath: r.URL.Path,
		Query:   r.URL.Query(),
		Header:  r.Header.Clone(),
		Body:    append([]byte(nil), body...),
		Sub:     sub,
	}
	s.recMu.Lock()
	defer s.recMu.Unlock()
	s.reqSeq++
	rec.Seq = int(s.reqSeq)
	s.records = append(s.records, rec)
	return rec
}

// Requests returns every recorded request in arrival order.
func (s *Server) Requests() []*RecordedRequest {
	s.recMu.Lock()
	defer s.recMu.Unlock()
	out := make([]*RecordedRequest, len(s.records))
	copy(out, s.records)
	return out
}

// LastRequest returns the most recent recorded request, or nil.
func (s *Server) LastRequest() *RecordedRequest {
	s.recMu.Lock()
	defer s.recMu.Unlock()
	if len(s.records) == 0 {
		return nil
	}
	return s.records[len(s.records)-1]
}

// RequestsFor returns the recorded requests whose method and Graph path match.
// Query parameters are ignored, so a test can find "/me/chats" whatever the
// $top was.
func (s *Server) RequestsFor(method, path string) []*RecordedRequest {
	var out []*RecordedRequest
	for _, rec := range s.Requests() {
		if rec.Method == method && rec.Path == path {
			out = append(out, rec)
		}
	}
	return out
}

// ResetRequests clears the recorder without touching the seed. It is useful in
// a long test that asserts on one call at a time.
func (s *Server) ResetRequests() {
	s.recMu.Lock()
	defer s.recMu.Unlock()
	s.records = nil
}

// statusWriter captures the status and body while passing writes through.
type statusWriter struct {
	http.ResponseWriter
	status  int
	written bytes.Buffer
}

func newStatusWriter(w http.ResponseWriter) *statusWriter {
	return &statusWriter{ResponseWriter: w}
}

func (w *statusWriter) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	w.written.Write(b)
	return w.ResponseWriter.Write(b)
}

func (w *statusWriter) statusCode() int {
	if w.status == 0 {
		return http.StatusOK
	}
	return w.status
}

func (w *statusWriter) bodyBytes() []byte { return w.written.Bytes() }

// now returns the injected clock's time, used for objects created through the
// API so tests can freeze them.
func (s *Server) now() time.Time { return s.clk.Now().UTC() }
