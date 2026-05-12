package semantik

import (
	"errors"
	"net/http"
	"testing"
)

func TestHealth_OK(t *testing.T) {
	_, c := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != pathHealth {
			t.Errorf("path = %q, want %q", r.URL.Path, pathHealth)
		}
		if r.Method != http.MethodPost {
			t.Errorf("method = %q, want POST", r.Method)
		}
		w.WriteHeader(http.StatusOK)
	})
	if err := c.Health(t.Context()); err != nil {
		t.Fatalf("Health: %v", err)
	}
}

func TestHealth_ServerError(t *testing.T) {
	_, c := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		writeError(t, w, http.StatusInternalServerError, CodeInternalError, "down", 0)
	})
	err := c.Health(t.Context())
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, ErrInternal) {
		t.Errorf("want ErrInternal, got %v", err)
	}
}
