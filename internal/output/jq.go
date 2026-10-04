package output

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/itchyny/gojq"
)

// ApplyJQ runs a jq expression over JSON-encoded data and writes the results to
// out, one per line: a string result prints unquoted (which is what a script
// wants) and anything else prints as compact JSON. It implements the --jq flag,
// which is documented in PLAN.md's output conventions next to --json.
func ApplyJQ(w io.Writer, expr string, data []byte) error {
	query, err := gojq.Parse(expr)
	if err != nil {
		return &Error{Code: CodeUsage, Msg: "invalid --jq expression: " + err.Error()}
	}
	code, err := gojq.Compile(query)
	if err != nil {
		return &Error{Code: CodeUsage, Msg: "invalid --jq expression: " + err.Error()}
	}
	var input any
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	if err := dec.Decode(&input); err != nil {
		// An empty document is not an error: there is simply nothing to filter.
		if errors.Is(err, io.EOF) {
			return nil
		}
		return &Error{Code: CodeError, Msg: "decode JSON for --jq", Err: err}
	}
	iter := code.Run(input)
	for {
		value, ok := iter.Next()
		if !ok {
			return nil
		}
		if err, isErr := value.(error); isErr {
			return &Error{Code: CodeError, Msg: "--jq failed: " + err.Error()}
		}
		if s, isString := value.(string); isString {
			if _, err := fmt.Fprintln(w, s); err != nil {
				return &Error{Code: CodeError, Msg: "write --jq output", Err: err}
			}
			continue
		}
		enc := json.NewEncoder(w)
		enc.SetEscapeHTML(false)
		if err := enc.Encode(value); err != nil {
			return &Error{Code: CodeError, Msg: "encode --jq output", Err: err}
		}
	}
}
