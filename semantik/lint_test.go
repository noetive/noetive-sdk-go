package semantik

import (
	"errors"
	"net/http"
	"testing"
)

func TestLint_HappyPath(t *testing.T) {
	_, c := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if ct := r.Header.Get("Content-Type"); ct != "application/json" {
			t.Errorf("Content-Type = %q", ct)
		}
		// Spec requires lint to be unauthenticated (security: []);
		// the SDK must not emit the Authorization header on this path.
		if got := r.Header.Get("Authorization"); got != "" {
			t.Errorf("Lint must not send Authorization header, got %q", got)
		}
		var req LintRequest
		readJSON(t, r, &req)
		writeJSON(t, w, http.StatusOK, LintResponse{
			Valid:      true,
			Normalized: `MATCH DISTANCE("x") WITHIN 0.4`,
		})
	})
	res, err := c.Lint(t.Context(), LintRequest{Query: `MATCH DISTANCE("x") WITHIN 0.4`})
	if err != nil {
		t.Fatalf("Lint: %v", err)
	}
	if !res.Valid || res.Normalized == "" {
		t.Errorf("unexpected response: %+v", res)
	}
}

func TestLint_PreflightEmptyQuery(t *testing.T) {
	c, _ := New(testKey)
	_, err := c.Lint(t.Context(), LintRequest{})
	if !errors.Is(err, ErrInvalidRequest) {
		t.Errorf("want ErrInvalidRequest, got %v", err)
	}
}

func TestLint_PreflightCursorBounds(t *testing.T) {
	c, _ := New(testKey)
	_, err := c.Lint(t.Context(), LintRequest{Query: "abc", Cursor: -1})
	if !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("want ErrInvalidRequest for negative cursor, got %v", err)
	}
	_, err = c.Lint(t.Context(), LintRequest{Query: "abc", Cursor: 4})
	if !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("want ErrInvalidRequest for out-of-bounds cursor, got %v", err)
	}
}
