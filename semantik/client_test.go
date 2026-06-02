package semantik

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	gojson "github.com/goccy/go-json"
)

// testKey is a non-empty, server-invalid API key used to satisfy the
// New() non-empty check in unit tests.
const testKey = "keyu_000000000000000000000000000000000000000000000"

// newTestServer wires an httptest.Server to handler and returns a
// Client that targets it. The caller is responsible for server.Close.
func newTestServer(t *testing.T, handler http.HandlerFunc) (*httptest.Server, *Client) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	c, err := New(testKey, WithBaseURL(srv.URL))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return srv, c
}

// readJSON decodes the request body into v or fails the test.
func readJSON(t *testing.T, r *http.Request, v any) {
	t.Helper()
	data, err := io.ReadAll(r.Body)
	if err != nil {
		t.Fatalf("read request body: %v", err)
	}
	if err := gojson.Unmarshal(data, v); err != nil {
		t.Fatalf("unmarshal request body: %v\nraw: %s", err, data)
	}
}

// writeJSON writes v as JSON with the given status.
func writeJSON(t *testing.T, w http.ResponseWriter, status int, v any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := gojson.NewEncoder(w).Encode(v); err != nil {
		t.Fatalf("write response: %v", err)
	}
}

// writeError writes an ErrorResponse-shaped JSON body.
func writeError(t *testing.T, w http.ResponseWriter, status int, code, msg string, retryAfterMs uint32) {
	t.Helper()
	body := map[string]any{"error": code}
	if msg != "" {
		body["message"] = msg
	}
	if retryAfterMs > 0 {
		body["retry_after_ms"] = retryAfterMs
	}
	writeJSON(t, w, status, body)
}

func TestNew_RejectsEmptyKey(t *testing.T) {
	// Only empty / whitespace-only keys are rejected. The SDK does not
	// inspect the prefix or contents, so an unset key fails fast while
	// any non-empty key is left for the server to validate.
	for _, bad := range []string{"", " ", "\t\n"} {
		if _, err := New(bad); !errors.Is(err, ErrInvalidAPIKey) {
			t.Errorf("New(%q) = %v, want ErrInvalidAPIKey", bad, err)
		}
	}
}

func TestNew_AcceptsAnyNonEmptyKey(t *testing.T) {
	// No prefix check: the recognised keyu_/keyt_ prefixes and any other
	// non-empty shape are all accepted, so a future key family cannot be
	// rejected client-side.
	for _, ok := range []string{"keyu_abc", "keyt_abc", "sk-foo", "some_new_format_key"} {
		if _, err := New(ok); err != nil {
			t.Errorf("New(%q) rejected a non-empty key: %v", ok, err)
		}
	}
}

func TestNew_UserAgentHasVersion(t *testing.T) {
	var captured string
	_, c := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		captured = r.Header.Get("User-Agent")
		writeJSON(t, w, http.StatusOK, map[string]any{})
	})
	_ = c.Health(t.Context())
	if !strings.HasPrefix(captured, "noetive-sdk-go/") {
		t.Errorf("UA %q missing SDK prefix", captured)
	}
	if !strings.Contains(captured, Version) {
		t.Errorf("UA %q missing client Version %q", captured, Version)
	}
}

func TestNewFromEnv_MissingKey(t *testing.T) {
	t.Setenv(envAPIKey, "")
	if _, err := NewFromEnv(); err == nil {
		t.Fatal("expected error when NOETIVE_KEY_SECRET unset")
	}
}

func TestNewFromEnv_ReadsBaseURL(t *testing.T) {
	t.Setenv(envAPIKey, testKey)
	t.Setenv(envBaseURL, "https://staging.example/")
	c, err := NewFromEnv()
	if err != nil {
		t.Fatalf("NewFromEnv: %v", err)
	}
	if c.baseURL != "https://staging.example" {
		t.Errorf("baseURL = %q, want trimmed staging URL", c.baseURL)
	}
}
