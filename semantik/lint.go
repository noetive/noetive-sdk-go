package semantik

import "context"

// LintRequest is the body of POST /v1/lint. Cursor is a byte offset
// into Query; the zero value means "end of query".
//
// Field ordering: string (16 B) > int (8 B).
type LintRequest struct {
	Query  string `json:"query"`
	Cursor int    `json:"cursor,omitempty"`
}

// LintResponse carries parse diagnostics and completion suggestions.
type LintResponse struct {
	Normalized  string           `json:"normalized,omitempty"`
	Diagnostics []LintDiagnostic `json:"diagnostics,omitempty"`
	Completions []LintCompletion `json:"completions,omitempty"`
	Valid       bool             `json:"valid,omitempty"`
}

// LintDiagnostic reports a parse or validation error.
//
// Field ordering: string (16 B) > int (8 B).
type LintDiagnostic struct {
	Severity string `json:"severity,omitempty"`
	Message  string `json:"message,omitempty"`
	Line     int    `json:"line,omitempty"`
	Col      int    `json:"col,omitempty"`
	EndLine  int    `json:"end_line,omitempty"`
	EndCol   int    `json:"end_col,omitempty"`
}

// LintCompletion is a single auto-complete suggestion.
type LintCompletion struct {
	Label  string `json:"label,omitempty"`
	Kind   string `json:"kind,omitempty"`
	Detail string `json:"detail,omitempty"`
}

// Lint validates a SemQL query and returns diagnostics + completions.
// The endpoint is unauthenticated per the API spec (security: []); the
// SDK does not send the Authorization header for Lint calls.
func (c *Client) Lint(ctx context.Context, req LintRequest) (LintResponse, error) {
	if err := req.validate(); err != nil {
		return LintResponse{}, err
	}
	var resp LintResponse
	if err := c.doJSON(ctx, pathLint, req, &resp, authNone); err != nil {
		return LintResponse{}, err
	}
	return resp, nil
}

func (r LintRequest) validate() *Error {
	if r.Query == "" {
		return preflightErr("lint query must not be empty")
	}
	if r.Cursor < 0 {
		return preflightErr("lint cursor must not be negative")
	}
	if r.Cursor > len(r.Query) {
		return preflightErr("lint cursor %d out of bounds (query length %d)", r.Cursor, len(r.Query))
	}
	return nil
}
