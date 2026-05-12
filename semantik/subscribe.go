package semantik

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"iter"
	"net/http"
	"strings"
	"sync"

	"github.com/noetive/noetive-sdk-go/internal/sse"
)

// SubscribeRequest is the body of POST /v1/subscribe.
//
// Defaults apply when targeting the global configuration:
//
//   - Namespace empty ⇒ [DefaultNamespace] ("global")
//   - Model empty + namespace is global ⇒ [DefaultModel]
//   - Dimensions zero + namespace is global ⇒ [DefaultDimensions]
//
// A minimal request is therefore SubscribeRequest{Query: "..."}.
// Private namespaces require dashboard configuration and incur usage
// charges; callers using one MUST set Model and Dimensions explicitly.
//
// Field ordering: strings (16 B each) > uint16 (2 B).
type SubscribeRequest struct {
	Query      string `json:"query"`
	Namespace  string `json:"namespace"`
	Model      string `json:"model"`
	Dimensions uint16 `json:"dimensions"`
}

// SubscribedEvent is the payload of the first SSE frame, delivered
// before any match events.
type SubscribedEvent struct {
	SubscriptionID string `json:"subscription_id"`
}

// MatchEvent is the payload of each "match" SSE frame.
//
// Field ordering: string (16 B) > float32 (4 B).
type MatchEvent struct {
	MessageID string  `json:"message_id"`
	Score     float32 `json:"score"`
}

// Subscription is a live SSE match stream. It is safe to call [Close]
// from any goroutine; [Next] is single-reader and must not be called
// concurrently with itself.
//
// Field ordering: interface (16 B) > pointer (8 B) > func (8 B) >
// sync.Once (16 B) > string (16 B) > error (16 B).
type Subscription struct {
	body    io.ReadCloser
	scanner *sse.Scanner
	cancel  context.CancelFunc
	closed  sync.Once
	id      string
	err     error
}

// ID returns the server-assigned subscription identifier, populated
// before [Client.Subscribe] returns.
func (s *Subscription) ID() string { return s.id }

// Next returns the next match event. It blocks until one is available,
// the server closes the stream (io.EOF), the stream context passed to
// [Client.Subscribe] is cancelled, or a transport/parse error occurs.
//
// ctx on this method is a courtesy check only: it is sampled once on
// entry, then ignored while a read is in flight. To enforce a
// timeout, cancel the ctx passed to [Client.Subscribe] or call
// [Subscription.Close].
//
// # Error types
//
// Stream-time failures (transport drop, malformed frame) are
// returned as [*SubscribeStreamError] wrapping the underlying cause.
// The SDK never auto-reconnects mid-stream: a silent reconnect
// would drop matches between the old and new subscription_id.
// Callers that want to reconnect must do so explicitly and dedupe
// on [MatchEvent.MessageID].
//
// [io.EOF] (a clean server close) and [context.Canceled] /
// [context.DeadlineExceeded] surface unchanged so existing
// [errors.Is] checks against them keep working.
func (s *Subscription) Next(ctx context.Context) (MatchEvent, error) {
	if s.err != nil {
		return MatchEvent{}, s.err
	}
	// Sample ctx once on entry; the underlying bufio.Scanner.Scan does
	// not honour context cancellation for blocking reads — the stream
	// context wired up by Subscribe is what unblocks a stuck read.
	if err := ctx.Err(); err != nil {
		return MatchEvent{}, err
	}
	for {
		if !s.scanner.Scan() {
			if err := s.scanner.Err(); err != nil {
				wrapped := wrapSubscribeStreamError(err)
				s.err = wrapped
				return MatchEvent{}, wrapped
			}
			// Clean close — leave raw so errors.Is(err, io.EOF) works.
			s.err = io.EOF
			return MatchEvent{}, io.EOF
		}
		f := s.scanner.Frame()
		if f.Event != "match" {
			// Skip unknown event types rather than failing — forward-compat.
			continue
		}
		var ev MatchEvent
		if err := safeUnmarshal([]byte(f.Data), &ev); err != nil {
			wrapped := wrapSubscribeStreamError(&MalformedSSEError{Err: err})
			s.err = wrapped
			return MatchEvent{}, wrapped
		}
		return ev, nil
	}
}

// wrapSubscribeStreamError converts a raw mid-stream error into a
// [*SubscribeStreamError]. The structured Err field is synthesised
// from a [*MalformedSSEError] cause when present; for transport
// failures it carries [CodeMalformedResponse] as a thin
// "something went wrong on the wire" marker. The Cause field carries
// the original error verbatim so [errors.Is] /[errors.As] resolve
// through the Unwrap chain.
func wrapSubscribeStreamError(err error) error {
	if err == nil {
		return nil
	}
	// Don't double-wrap if some inner layer already produced one.
	var already *SubscribeStreamError
	if errors.As(err, &already) {
		return err
	}
	return &SubscribeStreamError{
		Err:   &Error{Code: CodeMalformedResponse, Message: err.Error()},
		Cause: err,
	}
}

// Events adapts Next into a range-over-func iterator. Callers MUST
// still invoke [Subscription.Close] — breaking out of the loop does
// not release the underlying HTTP connection on its own. When yield
// receives a non-nil error, subsequent iterations return immediately.
//
// Usage:
//
//	sub, err := c.Subscribe(ctx, req)
//	if err != nil { return err }
//	defer sub.Close()            // ← required, in all paths
//	for ev, err := range sub.Events(ctx) {
//	    if err != nil { break }
//	    handle(ev)
//	}
func (s *Subscription) Events(ctx context.Context) iter.Seq2[MatchEvent, error] {
	return func(yield func(MatchEvent, error) bool) {
		for {
			ev, err := s.Next(ctx)
			if !yield(ev, err) {
				return
			}
			if err != nil {
				return
			}
		}
	}
}

// Close releases the HTTP connection and stops the scanner. Safe to
// call from any goroutine; idempotent.
func (s *Subscription) Close() error {
	s.closed.Do(func() {
		if s.cancel != nil {
			s.cancel()
		}
		if s.body != nil {
			_ = s.body.Close()
		}
	})
	return nil
}

// Subscribe registers a persistent subscription and opens an SSE match
// stream. The initial "subscribed" frame is consumed inside Subscribe
// so that [Subscription.ID] is populated on the returned value; auth
// and transport errors surface here rather than being deferred to the
// first Next call.
//
// # Handshake retry
//
// The handshake (POST + content-type validation + first frame read)
// is wrapped in the client's configured [RetryPolicy] — the same
// policy applied to one-shot RPCs. A 503 unavailable with the
// message "subscription setup did not complete within the budget"
// is the most common transient at this stage and the server's
// retry_after_ms hint is honoured. Replay is safe because each
// handshake either succeeds with a fresh subscription_id or fails
// before the server commits state, so no events can be missed.
//
// Mid-stream disconnects are NOT auto-retried — see
// [SubscribeStreamError]. Reconnecting silently would drop matches
// between the old and new subscription_id; the caller decides
// whether and how to reconnect.
//
// # Error types
//
// A terminal handshake failure is returned as a [*SubscribeSetupError]
// wrapping the underlying [*Error] or transport error; mid-stream
// failures from [Subscription.Next] surface as
// [*SubscribeStreamError]. Both types implement [errors.Unwrap] so
// existing [errors.Is] checks (e.g. against [ErrUnauthorized],
// [ErrMalformedSSE]) keep working.
//
// # Context lifecycle
//
// ctx bounds both the subscribe handshake AND the entire stream
// lifetime: cancelling the ctx passed here causes in-flight
// [Subscription.Next] reads to unblock. The ctx handed to
// [Subscription.Next] / [Subscription.Events] is a separate,
// per-call affordance that is only sampled on entry — it cannot
// abort a read that is already blocked inside the scanner (see
// [Subscription.Next] for the full contract).
//
// A single long-lived signal-aware context (e.g. one tied to the
// process's shutdown signal) is the common pattern; pair it with
// [Subscription.Close] for the orderly-teardown path.
//
// The caller MUST call [Subscription.Close] to release the connection,
// even when breaking out of an [Subscription.Events] loop early.
func (c *Client) Subscribe(ctx context.Context, req SubscribeRequest) (*Subscription, error) {
	if req.Namespace == "" {
		req.Namespace = DefaultNamespace
	}
	applyNamespaceDefaults(req.Namespace, &req.Model, &req.Dimensions)
	if err := req.validate(); err != nil {
		// Validation failures predate the retry loop and produce no
		// server state to clean up. Wrap so callers can branch on
		// SubscribeSetupError uniformly.
		return nil, &SubscribeSetupError{Err: err, Cause: err}
	}
	body, release, err := encodeJSON(req)
	if err != nil {
		return nil, &SubscribeSetupError{
			Err:   &Error{Code: CodeMalformedResponse, Message: err.Error()},
			Cause: fmt.Errorf("semantik: encode subscribe: %w", err),
		}
	}
	defer release()

	sub, err := runWithRetryValue(ctx, c.retry, func(_ int) (*Subscription, error) {
		return c.subscribeOnce(ctx, body)
	})
	if err != nil {
		return nil, wrapSubscribeSetupError(err)
	}
	return sub, nil
}

// subscribeOnce performs one handshake attempt: POST, validate the
// content-type, read until the initial "subscribed" frame, and return
// a ready-to-stream [*Subscription]. Errors are returned in their
// natural shape (raw [*Error] for HTTP failures so the retry policy
// can examine [Error.Code] / [Error.RetryAfter]; raw
// [*MalformedSSEError] or transport errors otherwise) and wrapped in
// [*SubscribeSetupError] by the caller after the retry loop exits.
//
// Each attempt owns its own derived context so a failed attempt's
// cancel does not affect the next one; on success the caller takes
// ownership of the cancel via the returned Subscription.
func (c *Client) subscribeOnce(parentCtx context.Context, body []byte) (*Subscription, error) {
	streamCtx, cancel := context.WithCancel(parentCtx)

	httpReq, err := http.NewRequestWithContext(streamCtx, http.MethodPost, c.baseURL+pathSubscribe, bytes.NewReader(body))
	if err != nil {
		cancel()
		return nil, err
	}
	httpReq.Header.Set(headerContentType, mimeJSON)
	httpReq.Header.Set(headerAccept, mimeSSE)
	httpReq.Header.Set(headerUserAgent, UserAgent())
	httpReq.Header.Set(headerAuthorization, c.authHeader)
	// Refuse gzip for SSE: compression on a line-delimited real-time
	// protocol buys nothing and can delay frame delivery. Setting any
	// Accept-Encoding value also disables net/http's transparent gzip
	// so the scanner sees bytes verbatim.
	httpReq.Header.Set("Accept-Encoding", "identity")
	httpReq.ContentLength = int64(len(body))

	resp, err := c.http.Do(httpReq)
	if err != nil {
		cancel()
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		defer drainAndClose(resp.Body)
		cancel()
		return nil, decodeError(resp)
	}
	// Enforce the SSE content-type per the public-api.yaml spec. A
	// misconfigured server or proxy returning a JSON or HTML body with
	// 200 would otherwise feed garbage through the scanner.
	if !isEventStreamContentType(resp.Header.Get(headerContentType)) {
		drainAndClose(resp.Body)
		cancel()
		return nil, &MalformedSSEError{Err: fmt.Errorf(
			"expected %s, got %q", mimeSSE, resp.Header.Get(headerContentType))}
	}

	scanner := sse.NewScanner(resp.Body)
	if !scanner.Scan() {
		scanErr := scanner.Err()
		drainAndClose(resp.Body)
		cancel()
		if scanErr == nil {
			scanErr = io.ErrUnexpectedEOF
		}
		return nil, fmt.Errorf("semantik: subscribe: read subscribed frame: %w", scanErr)
	}
	f := scanner.Frame()
	if f.Event != "subscribed" {
		drainAndClose(resp.Body)
		cancel()
		return nil, &MalformedSSEError{Err: fmt.Errorf("expected 'subscribed' event, got %q", f.Event)}
	}
	var sub SubscribedEvent
	if err := safeUnmarshal([]byte(f.Data), &sub); err != nil {
		drainAndClose(resp.Body)
		cancel()
		return nil, &MalformedSSEError{Err: err}
	}
	if sub.SubscriptionID == "" {
		drainAndClose(resp.Body)
		cancel()
		return nil, &MalformedSSEError{Err: errors.New("subscribed frame missing subscription_id")}
	}

	return &Subscription{
		body:    resp.Body,
		scanner: scanner,
		cancel:  cancel,
		id:      sub.SubscriptionID,
	}, nil
}

// wrapSubscribeSetupError converts a raw handshake-time error into a
// [*SubscribeSetupError], preserving structured *Error envelopes when
// available and the raw cause for transport / parse failures so
// errors.Is and errors.As resolve through the Unwrap chain.
func wrapSubscribeSetupError(err error) error {
	if err == nil {
		return nil
	}
	// Already wrapped (e.g. validation failure path).
	var setup *SubscribeSetupError
	if errors.As(err, &setup) {
		return err
	}
	var apiErr *Error
	if errors.As(err, &apiErr) {
		return &SubscribeSetupError{Err: apiErr, Cause: err}
	}
	// Transport / malformed-SSE / encode failures: synthesise a thin
	// *Error for the structured Err field so callers that always
	// expect Err != nil don't have to nil-check, but keep the raw
	// cause intact for Unwrap.
	return &SubscribeSetupError{
		Err:   &Error{Code: CodeMalformedResponse, Message: err.Error()},
		Cause: err,
	}
}

func (r SubscribeRequest) validate() *Error {
	if r.Query == "" {
		return preflightErr("subscribe query must not be empty")
	}
	if r.Model == "" {
		return preflightErr("subscribe model must not be empty")
	}
	return validateDimensions(r.Dimensions)
}

// isEventStreamContentType reports whether ct is a valid
// text/event-stream media type, tolerating the "; charset=..." or
// other RFC 9110 parameters a proxy might append. Comparison is
// case-insensitive per RFC 9110 §5.6.
func isEventStreamContentType(ct string) bool {
	if ct == "" {
		return false
	}
	// Trim any parameters (e.g. "; charset=utf-8").
	if i := strings.IndexByte(ct, ';'); i >= 0 {
		ct = ct[:i]
	}
	ct = strings.TrimSpace(ct)
	return strings.EqualFold(ct, mimeSSE)
}
