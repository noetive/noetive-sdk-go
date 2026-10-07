package bud_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"go.noetive.io/noetive-sdk-go/bud"
)

// What a caller sees, and the one distinction the whole package turns on: a
// refusal is a value, a transport failure is an error.

// TestARefusalIsAValueNotAnError is the central contract.
//
// The server answers a rejected request with the operation's own output, at a
// status derived from the code. Returning that as a Go error would make every
// caller rebuild the envelope in order to read the things it needs — the code, the
// hint, whether to retry — out of a value it was already handed.
func TestARefusalIsAValueNotAnError(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-Request-Id", "request_01abc")
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"ref":"message_01x","error":{` +
			`"code":"precondition_failed","message":"other writes to this object kept landing first",` +
			`"hint":"read it and try again"}}`))
	}))
	defer srv.Close()

	c := mustClient(t, srv.URL)

	out, err := c.UpdateMessage(t.Context(), bud.Change{
		ID: "message_01x", Changes: json.RawMessage(`{"labels":["urgent"]}`),
	})
	if err != nil {
		t.Fatalf("a refusal arrived as a Go error, so the caller gets no envelope: %v", err)
	}
	if out.Error == nil {
		t.Fatal("the refusal was dropped")
	}

	if out.Error.Code != bud.CodePreconditionFailed {
		t.Errorf("code = %q", out.Error.Code)
	}
	if out.Error.HTTPStatus != http.StatusConflict {
		t.Errorf("HTTPStatus = %d, want 409", out.Error.HTTPStatus)
	}
	// The request id is filled from the header when the body omits it, so a caller
	// always has a token to quote.
	if out.Error.RequestID != "request_01abc" {
		t.Errorf("RequestID = %q, want the header's", out.Error.RequestID)
	}

	if out.Error.Hint != "read it and try again" || !out.Error.Retryable() {
		t.Errorf("the refusal lost what to do next: hint %q, retryable %v", out.Error.Hint, out.Error.Retryable())
	}

	// errors.Is works on the sentinel, so a caller can branch without a switch on
	// a string literal.
	if !errors.Is(out.Error, bud.ErrPreconditionFailed) {
		t.Error("errors.Is did not match the sentinel")
	}
}

// TestARefusalReadsAsAPlan is criterion three, made checkable.
//
// "rate_limited" alone leaves a caller guessing. The fields that say what to do
// next cost nothing per turn — a refusal is read once — so they belong in the
// sentence the caller will actually see rather than behind a type assertion it may
// never make.
func TestARefusalReadsAsAPlan(t *testing.T) {
	t.Parallel()

	e := &bud.Error{
		Code: bud.CodeRateLimited, Message: "the per_hour limit is used up",
		Counter: "per_hour", RetryAfterMs: 41 * 60 * 1000,
		Hint: "wait for the window to reset", RequestID: "request_01x", HTTPStatus: 429,
	}

	got := e.Error()
	for _, want := range []string{"429", "rate_limited", "per_hour", "41m0s", "wait for the window", "request_01x"} {
		if !strings.Contains(got, want) {
			t.Errorf("the rendered refusal is missing %q:\n%s", want, got)
		}
	}
	if e.RetryAfter() != 41*time.Minute {
		t.Errorf("RetryAfter = %v", e.RetryAfter())
	}
	if !e.Retryable() {
		t.Error("a rate limit is retryable once it clears")
	}

	// The one that looks transient and is not: its own hint says a redeploy is
	// required, so retrying burns budget forever.
	unavailable := &bud.Error{Code: bud.CodeUnavailable}
	if unavailable.Retryable() {
		t.Error("unavailable must not be reported as retryable")
	}
}

// TestATransportFailureIsAnError is the other half.
//
// A body that is not the envelope — a load balancer's HTML, a proxy's plain text — is not a
// refusal the server authored, and presenting it as one would put words in the
// server's mouth. The status is kept because it still carries information.
func TestATransportFailureIsAnError(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte("<html><body>502 Bad Gateway</body></html>"))
	}))
	defer srv.Close()

	_, err := mustClient(t, srv.URL).DescribeMe(t.Context())
	if err == nil {
		t.Fatal("an HTML 502 was reported as success")
	}
	var apiErr *bud.Error
	if !errors.As(err, &apiErr) {
		t.Fatalf("want *bud.Error, got %T", err)
	}
	if apiErr.Code != bud.CodeMalformedResponse {
		t.Errorf("code = %q, want %q", apiErr.Code, bud.CodeMalformedResponse)
	}
	if apiErr.HTTPStatus != http.StatusBadGateway {
		t.Errorf("HTTPStatus = %d, want 502", apiErr.HTTPStatus)
	}
}

// TestPreflightRefusesBeforeSending covers the checks worth making locally.
//
// A request this package can tell is wrong costs a round trip to have refused, and
// the identifier prefix makes the commonest mistake — pasting one kind of id where
// another belongs — checkable without one. HTTPStatus stays zero, which is how a
// caller tells "we rejected this" from "the server did".
func TestPreflightRefusesBeforeSending(t *testing.T) {
	t.Parallel()

	var reached atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reached.Add(1)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()
	c := mustClient(t, srv.URL)

	// A thread identifier where a message one belongs.
	_, err := c.DescribeMessage(t.Context(), bud.ByID{ID: "thread_01x"})
	if err == nil {
		t.Fatal("a thread id was accepted for DescribeMessage")
	}
	var apiErr *bud.Error
	if !errors.As(err, &apiErr) || apiErr.HTTPStatus != 0 {
		t.Errorf("want a preflight refusal with HTTPStatus 0, got %v", err)
	}

	// A send with nothing to send.
	if _, err := c.Send(t.Context(), bud.SendInput{}); err == nil {
		t.Error("a send with no recipients and nothing to reply to was accepted")
	}
	// A change with no changes.
	if _, err := c.UpdateMessage(t.Context(), bud.Change{ID: "message_01x"}); err == nil {
		t.Error("an update with no Changes was accepted")
	}
	// A part read that names no part, and one aimed at a thread.
	if _, err := c.DescribePart(t.Context(), bud.ByID{ID: "message_01x"}); err == nil {
		t.Error("a part read with no Part was accepted")
	}
	if _, err := c.DescribePart(t.Context(), bud.ByID{ID: "thread_01x", Part: "2"}); err == nil {
		t.Error("a thread id was accepted for DescribePart")
	}
	// A mailbox is named by its agent, not by a message.
	if _, err := c.DescribeMailbox(t.Context(), bud.ByID{ID: "message_01x"}); err == nil {
		t.Error("a message id was accepted for DescribeMailbox")
	}
	if _, err := c.ListCorrespondents(t.Context(), bud.In{}); err == nil {
		t.Error("a correspondent listing with no mailbox was accepted")
	}
	// A listing names its mailbox by the agent; a thread id there is a mistake.
	if _, err := c.ListFolder(t.Context(), bud.In{In: "thread_01x"}); err == nil {
		t.Error("a thread id was accepted as a mailbox")
	}

	if n := reached.Load(); n != 0 {
		t.Errorf("%d requests reached the server; preflight is meant to refuse before sending", n)
	}
}

// TestAnUnkeyedWriteIsNeverRetried is the gate a retry policy cannot widen.
//
// A send re-issued without an idempotency key the caller chose sends a second copy.
// This package cannot invent one — a generated key would differ across a restart,
// so a genuine retry would duplicate while two distinct calls would collapse — so
// the rule is in code rather than in a comment.
func TestAnUnkeyedWriteIsNeverRetried(t *testing.T) {
	t.Parallel()

	var attempts atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		attempts.Add(1)
		// Hijack and close without answering, so the client sees a connection
		// failure rather than a refusal.
		hj, ok := w.(http.Hijacker)
		if !ok {
			t.Error("the test server cannot hijack")
			return
		}
		conn, _, err := hj.Hijack()
		if err != nil {
			return
		}
		_ = conn.Close()
	}))
	defer srv.Close()

	// A generous policy, to prove the gate and not the default.
	c, err := bud.New("t", bud.WithBaseURL(srv.URL),
		bud.WithRetry(bud.TransientRetry{Attempts: 5, Backoff: []time.Duration{time.Millisecond}}))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	attempts.Store(0)
	if _, err := c.Send(t.Context(), bud.SendInput{To: []string{"a@b.example"}}); err == nil {
		t.Fatal("a failed connection was reported as success")
	}
	if n := attempts.Load(); n != 1 {
		t.Errorf("an unkeyed send was attempted %d times; it must be attempted once", n)
	}

	// The same send with a key the caller chose is repeatable.
	attempts.Store(0)
	if _, err := c.Send(t.Context(), bud.SendInput{
		To: []string{"a@b.example"}, IdempotencyKey: "k1",
	}); err == nil {
		t.Fatal("a failed connection was reported as success")
	}
	if n := attempts.Load(); n < 2 {
		t.Errorf("a keyed send was attempted %d times; the key makes it retryable", n)
	}

	// A read is always repeatable.
	attempts.Store(0)
	if _, err := c.DescribeMe(t.Context()); err == nil {
		t.Fatal("a failed connection was reported as success")
	}
	if n := attempts.Load(); n < 2 {
		t.Errorf("a read was attempted %d times; reads are safe to repeat", n)
	}
}

// TestAForwardingClientHoldsNoCredential covers the relay case.
//
// The header comes from the context per call and the client holds none, so one
// caller's token cannot be served to another. A call with nothing in the context
// goes out bare — and the server's own refusal is the one to relay, because it is
// the one the agent knows how to act on.
func TestAForwardingClientHoldsNoCredential(t *testing.T) {
	t.Parallel()

	seen := make(chan string, 4)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen <- r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`{"ref":"me","kind":"me"}`))
	}))
	defer srv.Close()

	c, err := bud.Forwarding(bud.WithBaseURL(srv.URL))
	if err != nil {
		t.Fatalf("Forwarding: %v", err)
	}

	for _, want := range []string{"Bearer one", "Bearer two", ""} {
		ctx := t.Context()
		if want != "" {
			ctx = bud.WithAuthorizationContext(ctx, want)
		}
		if _, err := c.DescribeMe(ctx); err != nil {
			t.Fatalf("DescribeMe: %v", err)
		}
		if got := <-seen; got != want {
			t.Errorf("the backend saw %q, want %q", got, want)
		}
	}

	// And the client never prints what it carries, even under %#v.
	if s := c.GoString(); strings.Contains(s, "Bearer") {
		t.Errorf("GoString leaks a credential: %s", s)
	}
}

// TestARedirectIsRefused stops a 3xx relocating the credential.
//
// Following one would re-attach the Authorization header to whatever Location
// names, which is a host the caller never chose.
func TestARedirectIsRefused(t *testing.T) {
	t.Parallel()

	var elsewhere atomic.Int64
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			elsewhere.Add(1)
		}
		_, _ = w.Write([]byte(`{}`))
	}))
	defer other.Close()

	var redirects atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		redirects.Add(1)
		http.Redirect(w, r, other.URL+"/v1/me.describe", http.StatusTemporaryRedirect)
	}))
	defer srv.Close()

	// A retrying policy, to prove a redirect is an answer and not a failed
	// connection: it is refused once and not asked again.
	c, err := bud.New("test-token", bud.WithBaseURL(srv.URL),
		bud.WithRetry(bud.TransientRetry{Attempts: 2, Backoff: []time.Duration{time.Millisecond}}))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := c.DescribeMe(t.Context()); err == nil {
		t.Error("a redirect was followed")
	}
	if n := redirects.Load(); n != 1 {
		t.Errorf("the redirect was asked for %d times; a refused redirect is not retried", n)
	}
	if n := elsewhere.Load(); n != 0 {
		t.Errorf("the credential reached another host %d times", n)
	}
}

func mustClient(t *testing.T, base string) *bud.Client {
	t.Helper()
	c, err := bud.New("test-token", bud.WithBaseURL(base), bud.WithRetry(bud.NoRetry{}))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c
}

// alwaysRetry is a policy that would repeat anything, to prove the write gate
// is the client's and not the policy's.
type alwaysRetry struct{}

func (alwaysRetry) ShouldRetry(attempt int, _ string, _ any) bool { return attempt < 3 }
func (alwaysRetry) Wait(context.Context, int) error               { return nil }

// TestNoPolicyCanRetryAnUnkeyedWrite: WithRetry tunes the schedule; it cannot
// make an unkeyed send repeatable, whatever the installed policy says.
func TestNoPolicyCanRetryAnUnkeyedWrite(t *testing.T) {
	t.Parallel()

	var attempts atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		attempts.Add(1)
		if hj, ok := w.(http.Hijacker); ok {
			if conn, _, err := hj.Hijack(); err == nil {
				_ = conn.Close()
			}
		}
	}))
	defer srv.Close()

	c, err := bud.New("t", bud.WithBaseURL(srv.URL), bud.WithRetry(alwaysRetry{}))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := c.Send(t.Context(), bud.SendInput{To: []string{"a@b.example"}}); err == nil {
		t.Fatal("a failed connection was reported as success")
	}
	if n := attempts.Load(); n != 1 {
		t.Errorf("an unkeyed send was attempted %d times under a permissive policy; it must be once", n)
	}
}

// TestAResponseThatBrokeOffIsNotRetried: once a response has begun, the
// request reached the service, so repeating it could repeat its effect.
func TestAResponseThatBrokeOffIsNotRetried(t *testing.T) {
	t.Parallel()

	var attempts atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		attempts.Add(1)
		// Promise more body than is sent, so the read fails after the headers.
		w.Header().Set("Content-Length", "1000")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"kind":`))
	}))
	defer srv.Close()

	c, err := bud.New("t", bud.WithBaseURL(srv.URL),
		bud.WithRetry(bud.TransientRetry{Attempts: 3, Backoff: []time.Duration{time.Millisecond}}))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := c.DescribeMe(t.Context()); err == nil {
		t.Fatal("a truncated response was reported as success")
	}
	if n := attempts.Load(); n != 1 {
		t.Errorf("a response that broke off was retried: %d attempts", n)
	}
}
