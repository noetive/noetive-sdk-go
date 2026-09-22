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

	"github.com/noetive/noetive-sdk-go/bud"
)

// What a caller sees, and the one distinction the whole package turns on: a
// refusal is a value, a transport failure is an error.

// TestARefusalIsAValueNotAnError is the central contract.
//
// The server answers a rejected request with the operation's own output, at a
// status derived from the code. Returning that as a Go error would make every
// caller rebuild the envelope in order to read the things it needs — the code, the
// hint, the version to retry with — out of a value it was already handed.
func TestARefusalIsAValueNotAnError(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-Request-Id", "request_01abc")
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"ref":"thread_01x","error":{` +
			`"code":"precondition_failed","message":"version does not match",` +
			`"version":"\"v2\"","current":{"state":"open"},` +
			`"hint":"merge and quote the new version"}}`))
	}))
	defer srv.Close()

	c := mustClient(t, srv.URL)

	out, err := c.UpdateThread(t.Context(), bud.Change{
		ID: "thread_01x", Version: `"v1"`, Changes: json.RawMessage(`{"state":"closed"}`),
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

	// The point of Current: the retry is a merge, not a second read.
	var current struct{ State string }
	if err := out.Error.Into(&current); err != nil {
		t.Fatalf("Into: %v", err)
	}
	if current.State != "open" {
		t.Errorf("the conflict carried state %q", current.State)
	}
	if out.Error.Version != `"v2"` {
		t.Errorf("Version = %q, want the one to retry with", out.Error.Version)
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
// A body that is not the envelope — an ALB's HTML, a proxy's plain text — is not a
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
		t.Error("a send with no recipients and no draft was accepted")
	}
	// A change with no changes.
	if _, err := c.UpdateThread(t.Context(), bud.Change{ID: "thread_01x"}); err == nil {
		t.Error("an update with no Changes was accepted")
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

// TestAnEmptyPollIsSuccess pins the reading that stops a retry loop.
//
// Nothing happened within the timeout. A caller that treats this as failure and
// calls straight back spends the budget it was told to wait with, and the next poll
// is identical.
func TestAnEmptyPollIsSuccess(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"cursor":"41"}`))
	}))
	defer srv.Close()

	out, err := mustClient(t, srv.URL).Wait(t.Context(), bud.WaitInput{TimeoutSeconds: 1})
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if out.Error != nil {
		t.Fatalf("an empty poll carried a refusal: %v", out.Error)
	}
	if !out.Empty() {
		t.Error("Empty() is false for a poll that returned nothing")
	}
	if out.Cursor != "41" {
		t.Errorf("Cursor = %q; the position must survive an empty poll", out.Cursor)
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

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL+"/v1/me.describe", http.StatusTemporaryRedirect)
	}))
	defer srv.Close()

	if _, err := mustClient(t, srv.URL).DescribeMe(t.Context()); err == nil {
		t.Error("a redirect was followed")
	}
	if n := elsewhere.Load(); n != 0 {
		t.Errorf("the credential reached another host %d times", n)
	}
}

// TestTheClientSendsNoTimeoutOfItsOwn protects the long poll and the stream.
//
// http.Client.Timeout bounds the whole exchange including the body, so a client
// carrying one would sever a twenty-five second poll and any stream. The response
// header timeout is the right knob and it is well past the poll.
func TestTheClientSendsNoTimeoutOfItsOwn(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		// Answer the headers at once, then hold the body open past anything a
		// whole-exchange timeout would allow.
		w.WriteHeader(http.StatusOK)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		time.Sleep(300 * time.Millisecond)
		_, _ = w.Write([]byte(`{"cursor":"1"}`))
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	out, err := mustClient(t, srv.URL).Wait(ctx, bud.WaitInput{TimeoutSeconds: 1})
	if err != nil {
		t.Fatalf("a slow body was cut short: %v", err)
	}
	if out.Cursor != "1" {
		t.Errorf("Cursor = %q", out.Cursor)
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
