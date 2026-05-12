package semantik

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestSecurity_RefusesRedirectsPreservingAuth asserts that the SDK does
// NOT transparently follow a server-issued redirect. If it did, the
// default net/http behaviour would resend the Authorization bearer
// token to the redirect target — an exploit vector when an attacker
// controls the upstream host.
//
// The test wires two servers: an origin that replies 302 to the "evil"
// server, and an evil server that captures any Authorization header it
// receives. The SDK must surface an error on 302 and must never hit
// the evil server.
func TestSecurity_RefusesRedirectsPreservingAuth(t *testing.T) {
	var leaked string
	evil := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		leaked = r.Header.Get("Authorization")
		writeJSON(t, w, http.StatusOK, map[string]string{})
	}))
	t.Cleanup(evil.Close)

	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, evil.URL+"/v1/health", http.StatusFound)
	}))
	t.Cleanup(origin.Close)

	c, err := New(testKey, WithBaseURL(origin.URL))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := c.Health(t.Context()); err == nil {
		t.Fatal("Health followed a cross-host redirect; SDK must refuse and surface an error")
	}
	if leaked != "" {
		t.Fatalf("Authorization bearer was forwarded to redirect target: %q", leaked)
	}
}

// TestSecurity_ClientStringDoesNotLeakKey asserts that common
// debugging patterns (%v, %+v, %#v, %s) do not print the raw API key.
// Callers routinely printf a client from a panic or debugger; the SDK
// must not make credential leakage a one-keystroke mistake.
func TestSecurity_ClientStringDoesNotLeakKey(t *testing.T) {
	const secret = "keyu_" + "verysensitivesecretthatmustnotleak_000000000000"
	c, err := New(secret)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	for _, verb := range []string{"%v", "%+v", "%#v", "%s"} {
		got := fmt.Sprintf(verb, c)
		if strings.Contains(got, secret) {
			t.Errorf("fmt.Sprintf(%q, client) leaked API key: %s", verb, got)
		}
	}
}

// TestSecurity_DefaultTransportFallbackHasTimeout guards the code path
// where http.DefaultTransport is not an *http.Transport (non-standard
// builds, tests that swap it out). The SDK must still produce a
// transport with a ResponseHeaderTimeout so that a hostile server
// trickling headers cannot hang the caller indefinitely.
func TestSecurity_DefaultTransportFallbackHasTimeout(t *testing.T) {
	prev := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = prev })

	// A RoundTripper that is not *http.Transport forces the fallback
	// branch of defaultHTTPClient().
	http.DefaultTransport = roundTripperFunc(func(*http.Request) (*http.Response, error) {
		return nil, fmt.Errorf("unreachable")
	})

	hc := defaultHTTPClient()
	tr, ok := hc.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("fallback transport is %T, want *http.Transport so ResponseHeaderTimeout applies", hc.Transport)
	}
	if tr.ResponseHeaderTimeout == 0 {
		t.Error("fallback *http.Transport must carry a ResponseHeaderTimeout; got zero")
	}
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
