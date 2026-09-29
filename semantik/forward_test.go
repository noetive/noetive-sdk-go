package semantik_test

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"go.noetive.io/noetive-sdk-go/semantik"
)

// TestForwardingSendsWhatItWasGiven covers the relay case.
//
// Three things, and the third is the one that matters: a forwarding client with no
// credential must still reach the server. New() refuses that locally and is right
// to — a program configuring itself with an empty key has made a mistake. A relay
// has not: the request it is relaying simply had no header, and the refusal the
// caller needs is the server's own, with its request id and its explanation, not a
// client-side ErrInvalidAPIKey that Noetive never produced.
func TestForwardingSendsWhatItWasGiven(t *testing.T) {
	t.Parallel()

	seen := make(chan string, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen <- r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"results":[],"namespace":"n","model":"m","dimensions":1}`))
	}))
	defer srv.Close()

	for _, header := range []string{"Bearer keyu_abc", "SomeFutureScheme blob", ""} {
		c, err := semantik.NewForwarding(
			semantik.WithAuthorization(header),
			semantik.WithBaseURL(srv.URL),
			semantik.WithRetry(semantik.NoRetry{}),
		)
		if err != nil {
			t.Fatalf("NewForwarding(%q): %v", header, err)
		}
		// Search rather than Health: Health is one of the endpoints the spec
		// marks as needing no credential, so it would prove nothing about
		// forwarding one.
		if _, err := c.Search(t.Context(), semantik.SearchRequest{
			Query:     `MATCH DISTANCE("x") WITHIN 0.4`,
			Namespace: "n", Model: "m", Dimensions: 1,
		}); err != nil {
			t.Fatalf("Search with %q: %v", header, err)
		}
		if got := <-seen; got != header {
			t.Errorf("the backend saw %q, want %q unchanged", got, header)
		}
	}
}

// TestNewStillRefusesAnEmptyKey is the other half.
//
// NewForwarding loosening the check must not loosen it for New: a program that
// configured itself with no key should be told before it spends a round trip.
func TestNewStillRefusesAnEmptyKey(t *testing.T) {
	t.Parallel()

	if _, err := semantik.New(""); err == nil {
		t.Error("New accepted an empty key")
	}
	if _, err := semantik.New("   "); err == nil {
		t.Error("New accepted a whitespace key")
	}
}

// TestNewRefusesAForwardedCredential guards the seam between the two constructors.
//
// New builds its header from the key it was given, so a WithAuthorization passed
// alongside would otherwise be dropped without a word, and the caller would send a
// credential other than the one they chose.
func TestNewRefusesAForwardedCredential(t *testing.T) {
	t.Parallel()

	for _, header := range []string{"Bearer keyu_other", ""} {
		_, err := semantik.New("keyu_abc", semantik.WithAuthorization(header))
		if !errors.Is(err, semantik.ErrAuthorizationWithKey) {
			t.Errorf("New with WithAuthorization(%q): err = %v, want ErrAuthorizationWithKey", header, err)
		}
	}
}
