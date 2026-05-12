package semantik

import (
	"net/http"
	"time"
)

// Doer is the subset of *http.Client the SDK uses. Supplying a custom
// Doer via [WithHTTPClient] allows tests to inject round-trippers or
// transport middleware without wrapping the full [http.Client] type.
type Doer interface {
	Do(req *http.Request) (*http.Response, error)
}

// Option configures a [Client] during [New] or [NewFromEnv].
type Option func(*config)

// config holds the resolved client configuration. Unexported so that
// client state remains immutable after [New] returns.
//
// Field ordering: interfaces (16 B each) > string (16 B).
type config struct {
	httpClient Doer
	retry      RetryPolicy
	baseURL    string
}

// WithBaseURL overrides the default Semantik endpoint. Useful for
// staging environments or local servers. The URL must include scheme
// and host; a trailing slash is optional.
func WithBaseURL(url string) Option {
	return func(c *config) { c.baseURL = url }
}

// WithHTTPClient replaces the default [http.Client]. Pass a
// pre-configured client to customize timeouts, proxy behaviour, TLS
// settings, or transport middleware.
func WithHTTPClient(hc Doer) Option {
	return func(c *config) { c.httpClient = hc }
}

// WithRetry installs a retry policy for requests. The default is
// [TransientRetry](1) — one retry on documented transient codes
// (backpressure, unavailable, metering_unavailable, …), with a
// 100 ms / 2 s / 5 s / 10 s fallback when the server omits a retry
// hint. Pass [NoRetry]{} to opt out, or a different [TransientRetry]
// bound to tune the schedule. Only responses satisfying the policy's
// ShouldRetry method are retried.
//
// Subscribe semantics: the subscribe handshake (POST → first
// "subscribed" SSE frame) is retried under the same policy, since
// the handshake either succeeds with a fresh subscription_id or
// fails before any state is created — no events can be missed.
// Once the stream is open, mid-stream errors are surfaced unchanged
// (as a *SubscribeStreamError) for the caller to handle: a silent
// reconnect would drop matches between the old and new
// subscription_id.
func WithRetry(p RetryPolicy) Option {
	return func(c *config) { c.retry = p }
}

// defaultHTTPClient constructs a fresh [http.Client] with a cloned
// transport so the SDK does not share the process-wide
// [http.DefaultClient] connection pool. A 30 s response-header timeout
// bounds stalled dials without constraining long-running SSE bodies.
// Redirects are refused outright: following a 3xx would cause net/http
// to resend the Authorization bearer to the redirect target, which a
// hostile or compromised intermediary could use to capture the key.
func defaultHTTPClient() *http.Client {
	var t *http.Transport
	if base, ok := http.DefaultTransport.(*http.Transport); ok {
		t = base.Clone()
	} else {
		t = &http.Transport{}
	}
	t.ResponseHeaderTimeout = 30 * time.Second
	return &http.Client{
		Transport:     t,
		CheckRedirect: refuseRedirect,
	}
}

// refuseRedirect instructs [net/http] to return the 3xx response
// unchanged rather than following it. Returning [http.ErrUseLastResponse]
// suppresses the automatic redirect chase that would re-attach the
// Authorization header to the redirect target.
func refuseRedirect(*http.Request, []*http.Request) error {
	return http.ErrUseLastResponse
}
