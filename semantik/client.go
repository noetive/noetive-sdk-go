package semantik

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
)

const (
	headerAuthorization = "Authorization"
	headerContentType   = "Content-Type"
	headerUserAgent     = "User-Agent"
	headerAccept        = "Accept"

	mimeJSON = "application/json"
	mimeSSE  = "text/event-stream"

	envAPIKey  = "NOETIVE_KEY_SECRET"
	envBaseURL = "NOETIVE_BASE_URL"

	pathSearch    = "/v1/search"
	pathPublish   = "/v1/publish"
	pathLint      = "/v1/lint"
	pathSubscribe = "/v1/subscribe"
	pathHealth    = "/v1/health"
)

// Client is the Noetive Semantik API client. A Client is safe for
// concurrent use by multiple goroutines and is immutable after [New]
// returns.
//
// Field ordering: interface (16 B) > interface (16 B) > strings (16 B each).
type Client struct {
	http    Doer
	retry   RetryPolicy
	baseURL string
	// authHeader holds the precomputed "Bearer <key>" value. The raw
	// key is NOT retained separately so that a stray debug print of a
	// Client cannot leak it; see [Client.String] / [Client.GoString]
	// for the redaction these verbs route through.
	authHeader string
}

// String returns a redacted representation so that fmt.Sprintf("%v",
// c) cannot leak the API key into logs, panics, or debugger output.
func (c *Client) String() string {
	if c == nil {
		return "<nil>"
	}
	// %+v, %s and %v all route through here; the authHeader field is
	// never formatted directly.
	return fmt.Sprintf("semantik.Client{baseURL:%q, apiKey:REDACTED}", c.baseURL)
}

// GoString returns a redacted Go-syntax representation for the %#v
// verb.
func (c *Client) GoString() string {
	if c == nil {
		return "(*semantik.Client)(nil)"
	}
	return fmt.Sprintf("&semantik.Client{baseURL:%q, apiKey:REDACTED}", c.baseURL)
}

// New constructs a Client authenticated with the given API key. The key
// must be non-empty; the SDK does not inspect its prefix or contents, so
// an empty or whitespace-only key returns [ErrInvalidAPIKey]. Deeper
// validation is the server's job.
//
// Opts are applied in order; later values win.
func New(apiKey string, opts ...Option) (*Client, error) {
	if !apiKeyNonEmpty(apiKey) {
		return nil, ErrInvalidAPIKey
	}
	cfg := config{
		baseURL: defaultBaseURL,
	}
	for _, o := range opts {
		o(&cfg)
	}
	if cfg.httpClient == nil {
		cfg.httpClient = defaultHTTPClient()
	}
	if cfg.retry == nil {
		// Default policy: at least one retry on documented transient
		// codes. The SDK's working stance is "always retry at least
		// once" — short transient pushback is the common case in
		// production. Callers can widen with [TransientRetry] or opt
		// out with [NoRetry] via [WithRetry].
		cfg.retry = TransientRetry(1)
	}
	cfg.baseURL = strings.TrimRight(cfg.baseURL, "/")

	return &Client{
		http:       cfg.httpClient,
		retry:      cfg.retry,
		baseURL:    cfg.baseURL,
		authHeader: "Bearer " + apiKey,
	}, nil
}

// NewFromEnv constructs a Client using NOETIVE_KEY_SECRET and (optionally)
// NOETIVE_BASE_URL from the process environment. Returns
// [ErrMissingAPIKey] when NOETIVE_KEY_SECRET is unset or empty.
func NewFromEnv(opts ...Option) (*Client, error) {
	key := os.Getenv(envAPIKey)
	if key == "" {
		return nil, ErrMissingAPIKey
	}
	if base := os.Getenv(envBaseURL); base != "" {
		opts = append([]Option{WithBaseURL(base)}, opts...)
	}
	return New(key, opts...)
}

// apiKeyNonEmpty reports whether k is usable as an API key. The SDK
// checks only that the key is non-empty (ignoring surrounding
// whitespace); it does not inspect the prefix or contents. The server
// is the source of truth for key validity, and locking the client to a
// specific prefix shape would break the moment Noetive introduces a new
// key family.
func apiKeyNonEmpty(k string) bool {
	return strings.TrimSpace(k) != ""
}

// doJSON marshals req as JSON, POSTs it to path, and decodes a 2xx
// body into resp. Non-2xx responses are returned as *Error. Retry
// policy is applied when installed. auth controls whether the
// Authorization header is attached: pass authBearer for authenticated
// endpoints, authNone for endpoints the spec marks security: [].
func (c *Client) doJSON(ctx context.Context, path string, req, resp any, auth authMode) error {
	body, release, err := encodeJSON(req)
	if err != nil {
		return fmt.Errorf("semantik: encode request: %w", err)
	}
	defer release()

	return c.runWithRetry(ctx, func(attempt int) error {
		return c.sendOnce(ctx, path, body, resp, auth)
	})
}

// authMode controls whether a request carries the bearer token.
type authMode uint8

const (
	authNone   authMode = 0
	authBearer authMode = 1
)

// sendOnce performs a single HTTP round trip. body is a byte slice that
// is re-read on each retry (wrapped in a fresh [bytes.Reader]).
func (c *Client) sendOnce(ctx context.Context, path string, body []byte, resp any, auth authMode) error {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	httpReq.Header.Set(headerContentType, mimeJSON)
	httpReq.Header.Set(headerAccept, mimeJSON)
	httpReq.Header.Set(headerUserAgent, UserAgent())
	if auth == authBearer {
		httpReq.Header.Set(headerAuthorization, c.authHeader)
	}
	// ContentLength is known; set it so net/http avoids chunked encoding.
	httpReq.ContentLength = int64(len(body))

	httpResp, err := c.http.Do(httpReq)
	if err != nil {
		return err
	}
	defer drainAndClose(httpResp.Body)

	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		return decodeError(httpResp)
	}
	if resp == nil {
		return nil
	}
	if err := safeDecode(httpResp.Body, resp); err != nil {
		return &Error{
			Code:       CodeMalformedResponse,
			Message:    err.Error(),
			HTTPStatus: httpResp.StatusCode,
		}
	}
	return nil
}

// drainAndClose empties a response body up to a small cap and closes
// it, so that the underlying connection is returned to the idle pool.
//
// Errors from both the drain and the Close are intentionally ignored:
// they only report cleanup-time conditions (proxy reset, already-closed
// socket) that have no effect on the caller's outcome and carry no
// useful signal. This is a deliberate exception to the project's
// fail-fast rule; keep it local to the cleanup path.
func drainAndClose(r io.ReadCloser) {
	_, _ = io.Copy(io.Discard, io.LimitReader(r, 1<<14))
	_ = r.Close()
}
