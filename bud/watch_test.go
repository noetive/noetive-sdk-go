package bud_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"go.noetive.io/noetive-sdk-go/bud"
)

// The journal is served only as a stream: an `open` frame, then `batch` frames and
// `: keepalive` comments. These tests stand up that stream and check what Watch
// and Wait make of each part of it.

// stream is the server's side of one watch, written frame by frame.
type stream struct {
	w http.ResponseWriter
	r *http.Request
}

func (s stream) frame(event, data string) {
	_, _ = fmt.Fprintf(s.w, "event: %s\ndata: %s\n\n", event, data)
	s.w.(http.Flusher).Flush()
}

func (s stream) open(cursor string) { s.frame("open", `{"cursor":"`+cursor+`"}`) }

func (s stream) keepalive() {
	_, _ = io.WriteString(s.w, ": keepalive\n\n")
	s.w.(http.Flusher).Flush()
}

// hold keeps the stream open until the client leaves, as the server does. The
// bound only stops a broken test hanging the run.
func (s stream) hold() {
	select {
	case <-s.r.Context().Done():
	case <-time.After(10 * time.Second):
	}
}

// watchServer answers /v1/watch with a 200 event stream and runs script on it.
// Every request is checked for the shape the server accepts: a JSON POST asking
// for an event stream.
func watchServer(t *testing.T, script func(s stream)) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/watch" {
			t.Errorf("request %s %s, want POST /v1/watch", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Accept"); got != "text/event-stream" {
			t.Errorf("Accept = %q; the endpoint serves only an event stream", got)
		}
		if got := r.Header.Get("Content-Type"); got != "application/json" {
			t.Errorf("Content-Type = %q; the body is JSON", got)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
			t.Errorf("Authorization = %q; the stream needs the credential as much as any call", got)
		}
		if got := r.Header.Get("User-Agent"); !strings.HasPrefix(got, "noetive-sdk-go/") {
			t.Errorf("User-Agent = %q", got)
		}
		var in map[string]any
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			t.Errorf("the body is not JSON: %v", err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("X-Request-Id", "request_01stream")
		w.WriteHeader(http.StatusOK)
		script(stream{w: w, r: r})
	}))
	t.Cleanup(srv.Close)
	return srv
}

// refusingServer answers every request with a refusal envelope at status.
func refusingServer(t *testing.T, status int, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Request-Id", "request_01refused")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

const twoEvents = `{"cursor":"43","events":[` +
	`{"id":"journal_01a","type":"mail.received","mailbox":"ag_01x","message":"message_01a","seq":42},` +
	`{"id":"journal_01b","type":"mail.sent","mailbox":"ag_01x","message":"message_01b","seq":43,` +
	`"data":{"message_id":"abc@example.com"}}]}`

// TestWatchReadsTheStreamAsTheServerWritesIt walks every frame a healthy stream
// carries: the cursor from `open`, a keepalive that must not surface, and a batch
// whose events arrive in order and whose cursor replaces the one before.
func TestWatchReadsTheStreamAsTheServerWritesIt(t *testing.T) {
	t.Parallel()

	srv := watchServer(t, func(s stream) {
		s.open("41")
		s.keepalive()
		s.frame("presence", "not json; a frame this version does not know")
		s.frame("batch", twoEvents)
	})

	st, err := mustClient(t, srv.URL).Watch(t.Context(), bud.WaitInput{})
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}
	defer func() { _ = st.Close() }()

	if st.Cursor() != "41" {
		t.Errorf("Cursor after open = %q; the open frame says where the stream starts", st.Cursor())
	}
	if st.RequestID() != "request_01stream" {
		t.Errorf("RequestID = %q", st.RequestID())
	}

	var ids []string
	for {
		ev, ok := st.Next()
		if !ok {
			break
		}
		ids = append(ids, ev.ID)
	}
	if len(ids) != 2 || ids[0] != "journal_01a" || ids[1] != "journal_01b" {
		t.Errorf("events = %v, want both, in order, with the keepalive and the unknown frame skipped", ids)
	}
	if st.Cursor() != "43" {
		t.Errorf("Cursor after the batch = %q, want 43", st.Cursor())
	}
	if err := st.Err(); err != nil {
		t.Errorf("a clean close reported %v", err)
	}
}

// TestWatchEndsOnARefusalInFlight stops a stream that looks healthy while
// receiving nothing. The server ends after an error frame, and may send it with
// no cursor; the position the caller reconnects from must survive that.
func TestWatchEndsOnARefusalInFlight(t *testing.T) {
	t.Parallel()

	srv := watchServer(t, func(s stream) {
		s.open("41")
		s.frame("batch", `{"cursor":"","error":{"code":"internal","message":"the request could not be completed"}}`)
		s.hold()
	})

	st, err := mustClient(t, srv.URL).Watch(t.Context(), bud.WaitInput{})
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}
	defer func() { _ = st.Close() }()

	if _, ok := st.Next(); ok {
		t.Fatal("Next yielded an event from an error frame")
	}
	var refusal *bud.Error
	if !errors.As(st.Err(), &refusal) || refusal.Code != bud.CodeInternal {
		t.Fatalf("Err = %v, want the server's internal refusal", st.Err())
	}
	if refusal.RequestID != "request_01stream" {
		t.Errorf("RequestID = %q, want the stream's", refusal.RequestID)
	}
	if st.Cursor() != "41" {
		t.Errorf("Cursor = %q; an error frame without a cursor must not erase the position", st.Cursor())
	}
}

// TestWatchRefusesBeforeTheStreamOpens covers the request the server can judge
// wrong before answering 200: the refusal it sends is the one the caller gets.
func TestWatchRefusesBeforeTheStreamOpens(t *testing.T) {
	t.Parallel()

	srv := refusingServer(t, http.StatusForbidden,
		`{"error":{"code":"forbidden_scope","message":"this key does not reach an agent on this service"}}`)

	_, err := mustClient(t, srv.URL).Watch(t.Context(), bud.WaitInput{})
	var refusal *bud.Error
	if !errors.As(err, &refusal) {
		t.Fatalf("err = %v, want the server's refusal", err)
	}
	if !errors.Is(err, bud.ErrForbiddenScope) || refusal.HTTPStatus != http.StatusForbidden {
		t.Errorf("refusal = %v, want forbidden_scope at 403", refusal)
	}
	if refusal.RequestID != "request_01refused" {
		t.Errorf("RequestID = %q, want the header's", refusal.RequestID)
	}
}

// TestWaitReturnsTheFirstBatchWhole is the regression for a Wait that posted JSON
// to an endpoint that serves only a stream. It must read the stream, return the
// first batch with every event in it, and carry that batch's cursor — not the
// first event alone, and not wait for a second batch.
func TestWaitReturnsTheFirstBatchWhole(t *testing.T) {
	t.Parallel()

	srv := watchServer(t, func(s stream) {
		s.open("41")
		s.keepalive()
		s.frame("batch", twoEvents)
		s.frame("batch", `{"cursor":"44","events":[{"id":"journal_01c","type":"mail.read"}]}`)
		s.hold()
	})

	start := time.Now()
	out, err := mustClient(t, srv.URL).Wait(t.Context(), bud.WaitInput{Cursor: "41"})
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if out.Error != nil {
		t.Fatalf("Wait carried a refusal: %v", out.Error)
	}
	if len(out.Events) != 2 || out.Events[0].ID != "journal_01a" || out.Events[1].ID != "journal_01b" {
		t.Fatalf("events = %+v, want the whole first batch", out.Events)
	}
	if out.Events[1].Data.MessageID != "abc@example.com" {
		t.Errorf("MessageID = %q; it travels on the event's data", out.Events[1].Data.MessageID)
	}
	if out.Cursor != "43" {
		t.Errorf("Cursor = %q, want the first batch's", out.Cursor)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("Wait took %v; it must return on the first batch, not hold the window", elapsed)
	}
}

// TestAQuietWindowIsSuccess pins the reading that stops a retry loop.
//
// Nothing happened within the window. A caller that treats this as failure and
// calls straight back spends the budget it was told to wait with. The cursor is
// the one the stream opened at, so the next Wait misses nothing.
func TestAQuietWindowIsSuccess(t *testing.T) {
	t.Parallel()

	srv := watchServer(t, func(s stream) {
		s.open("41")
		for {
			select {
			case <-s.r.Context().Done():
				return
			case <-time.After(200 * time.Millisecond):
				s.keepalive()
			}
		}
	})

	start := time.Now()
	out, err := mustClient(t, srv.URL).Wait(t.Context(), bud.WaitInput{TimeoutSeconds: 1})
	if err != nil {
		t.Fatalf("a quiet window was reported as a failure: %v", err)
	}
	// Both ends: a Wait that returned at once would spin a caller's loop, and one
	// that ignored TimeoutSeconds would hold it for the full default.
	if elapsed := time.Since(start); elapsed < 900*time.Millisecond || elapsed > 3*time.Second {
		t.Errorf("a one-second window took %v", elapsed)
	}
	if !out.Empty() {
		t.Errorf("Empty() is false for a window with nothing in it: %+v", out)
	}
	if out.Cursor != "41" {
		t.Errorf("Cursor = %q; the position must survive a quiet window", out.Cursor)
	}
}

// TestWaitReturnsARefusalAsAValue holds Wait to the package's contract on both
// sides of the handshake: a refusal before the stream opens and one sent in
// flight are both answers, carried on the output with a nil error.
func TestWaitReturnsARefusalAsAValue(t *testing.T) {
	t.Parallel()

	t.Run("before the stream opens", func(t *testing.T) {
		t.Parallel()
		srv := refusingServer(t, http.StatusBadRequest,
			`{"error":{"code":"invalid","message":"the cursor does not parse","field":"/cursor"}}`)

		out, err := mustClient(t, srv.URL).Wait(t.Context(), bud.WaitInput{Cursor: "not-a-cursor"})
		if err != nil {
			t.Fatalf("a refusal arrived as a Go error: %v", err)
		}
		if out.Error == nil || out.Error.Code != bud.CodeInvalid || out.Error.Field != "/cursor" {
			t.Fatalf("Error = %v, want invalid at /cursor", out.Error)
		}
		if out.Error.HTTPStatus != http.StatusBadRequest || out.Error.RequestID != "request_01refused" {
			t.Errorf("status %d, request %q", out.Error.HTTPStatus, out.Error.RequestID)
		}
		if out.Cursor != "not-a-cursor" {
			t.Errorf("Cursor = %q, want the caller's own back", out.Cursor)
		}
	})

	t.Run("in flight", func(t *testing.T) {
		t.Parallel()
		srv := watchServer(t, func(s stream) {
			s.open("41")
			s.frame("batch", `{"cursor":"","error":{"code":"upstream_unavailable","message":"a dependency did not answer","request_id":"request_01frame"}}`)
			s.hold()
		})

		out, err := mustClient(t, srv.URL).Wait(t.Context(), bud.WaitInput{})
		if err != nil {
			t.Fatalf("a refusal arrived as a Go error: %v", err)
		}
		if out.Error == nil || out.Error.Code != bud.CodeUpstreamUnavailable {
			t.Fatalf("Error = %v, want upstream_unavailable", out.Error)
		}
		if out.Cursor != "41" {
			t.Errorf("Cursor = %q, want the stream's position, not the frame's empty one", out.Cursor)
		}
		if out.Error.HTTPStatus != http.StatusOK {
			t.Errorf("status %d; a refusal on an open stream arrived with the stream's 200, not before sending", out.Error.HTTPStatus)
		}
		if out.Error.RequestID != "request_01frame" {
			t.Errorf("RequestID = %q; the frame's own id names the failure", out.Error.RequestID)
		}
		if out.Empty() {
			t.Error("Empty() is true for a refusal; a caller would read it as a quiet window")
		}
	})
}

// TestWaitEndsWithTheCallersContext separates the caller giving up from a quiet
// window: the first is the caller's error, never an empty success.
func TestWaitEndsWithTheCallersContext(t *testing.T) {
	t.Parallel()

	srv := watchServer(t, func(s stream) {
		s.open("41")
		s.hold()
	})

	ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
	defer cancel()

	start := time.Now()
	out, err := mustClient(t, srv.URL).Wait(ctx, bud.WaitInput{TimeoutSeconds: 5})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, out = %+v; want the caller's deadline", err, out)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("Wait took %v; the caller's deadline was 200ms and must end it, not the window", elapsed)
	}
}

// TestWaitDoesNotReadAClosedStreamAsQuiet: a server that ends the stream with no
// batch is going away. An empty success would send the caller straight back.
func TestWaitDoesNotReadAClosedStreamAsQuiet(t *testing.T) {
	t.Parallel()

	srv := watchServer(t, func(s stream) { s.open("41") })

	_, err := mustClient(t, srv.URL).Wait(t.Context(), bud.WaitInput{TimeoutSeconds: 5})
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("err = %v, want io.ErrUnexpectedEOF", err)
	}
}

// TestTheClientSendsNoTimeoutOfItsOwn protects the window and the stream.
//
// http.Client.Timeout bounds the whole exchange including the body, so a client
// carrying one would sever a stream that is quiet for longer than it. The response
// header timeout is the right knob, and it stops at the headers.
func TestTheClientSendsNoTimeoutOfItsOwn(t *testing.T) {
	t.Parallel()

	srv := watchServer(t, func(s stream) {
		s.open("41")
		time.Sleep(300 * time.Millisecond)
		s.frame("batch", `{"cursor":"42","events":[{"id":"journal_01a"}]}`)
		s.hold()
	})

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	out, err := mustClient(t, srv.URL).Wait(ctx, bud.WaitInput{TimeoutSeconds: 3})
	if err != nil {
		t.Fatalf("a slow body was cut short: %v", err)
	}
	if out.Cursor != "42" {
		t.Errorf("Cursor = %q", out.Cursor)
	}
}

// TestAMalformedStreamIsNotAnAnswer: a stream that does not begin the way the
// server begins one, or a batch that does not decode, is this package failing to
// read the server — never an empty window, and never a refusal the server wrote.
func TestAMalformedStreamIsNotAnAnswer(t *testing.T) {
	t.Parallel()

	for name, script := range map[string]func(s stream){
		"no open frame":            func(s stream) { s.frame("batch", twoEvents) },
		"an open that is not JSON": func(s stream) { s.frame("open", "41") },
		"a batch that is not JSON": func(s stream) { s.open("41"); s.frame("batch", "{events") },
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			srv := watchServer(t, script)

			out, err := mustClient(t, srv.URL).Wait(t.Context(), bud.WaitInput{TimeoutSeconds: 5})
			if !errors.Is(err, bud.ErrMalformedResponse) {
				t.Errorf("err = %v, out = %+v; want malformed_response", err, out)
			}
			var e *bud.Error
			if errors.As(err, &e) && e.HTTPStatus == 0 {
				t.Error("a stream that could not be read reports status 0, which means refused before sending")
			}
		})
	}

	t.Run("a 200 that is not an event stream", func(t *testing.T) {
		t.Parallel()
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"cursor":"41"}`)
		}))
		t.Cleanup(srv.Close)

		if _, err := mustClient(t, srv.URL).Watch(t.Context(), bud.WaitInput{}); !errors.Is(err, bud.ErrMalformedResponse) {
			t.Errorf("err = %v; a JSON body on 200 is not a stream", err)
		}
	})

	t.Run("JSON that is not an envelope", func(t *testing.T) {
		t.Parallel()
		srv := refusingServer(t, http.StatusInternalServerError, `{"cursor":"41"}`)

		st, err := mustClient(t, srv.URL).Watch(t.Context(), bud.WaitInput{})
		if !errors.Is(err, bud.ErrMalformedResponse) || st != nil {
			t.Errorf("Watch = %v, %v; a failing status with no refusal in it is not a stream", st, err)
		}
	})

	t.Run("a gateway's page", func(t *testing.T) {
		t.Parallel()
		srv := refusingServer(t, http.StatusBadGateway, "<html>502 Bad Gateway</html>")

		out, err := mustClient(t, srv.URL).Wait(t.Context(), bud.WaitInput{})
		var e *bud.Error
		if !errors.As(err, &e) || e.Code != bud.CodeMalformedResponse || e.HTTPStatus != http.StatusBadGateway {
			t.Errorf("err = %v; a proxy's page is not the server's refusal", err)
		}
		if out.Error != nil {
			t.Errorf("out.Error = %v; words the server never wrote were presented as its refusal", out.Error)
		}
	})
}

// TestARefusalKeepsTheRequestIDItWasSentWith: the body's own id names the request
// that failed; the header's is the fallback, not an override.
func TestARefusalKeepsTheRequestIDItWasSentWith(t *testing.T) {
	t.Parallel()

	srv := refusingServer(t, http.StatusForbidden,
		`{"error":{"code":"forbidden_scope","message":"no mail scope","request_id":"request_01body"}}`)

	out, err := mustClient(t, srv.URL).Wait(t.Context(), bud.WaitInput{})
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if out.Error == nil || out.Error.RequestID != "request_01body" {
		t.Errorf("Error = %v, want the body's request id", out.Error)
	}
}

// TestCancellingEndsAWatch is how a caller stops a stream it no longer wants:
// the context the call was made with, as the documentation says.
func TestCancellingEndsAWatch(t *testing.T) {
	t.Parallel()

	srv := watchServer(t, func(s stream) {
		s.open("41")
		s.hold()
	})

	ctx, cancel := context.WithCancel(t.Context())
	st, err := mustClient(t, srv.URL).Watch(ctx, bud.WaitInput{})
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}
	defer func() { _ = st.Close() }()

	time.AfterFunc(100*time.Millisecond, cancel)
	start := time.Now()
	if _, ok := st.Next(); ok {
		t.Fatal("Next yielded an event from a stream that sent none")
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("Next returned after %v; cancelling must end the stream", elapsed)
	}
	if st.Err() == nil {
		t.Error("a cancelled stream reported a clean close")
	}
}

// TestWaitReleasesItsConnection: an agent loop calls Wait for ever, so a Wait
// that left its stream open would run the process out of connections one window
// at a time. The caller's context here never ends on its own, so only Wait can
// be what lets the server's side go.
func TestWaitReleasesItsConnection(t *testing.T) {
	t.Parallel()

	released := make(chan struct{})
	srv := watchServer(t, func(s stream) {
		s.open("41")
		s.frame("batch", twoEvents)
		<-s.r.Context().Done()
		close(released)
	})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := mustClient(t, srv.URL).Wait(ctx, bud.WaitInput{TimeoutSeconds: 5}); err != nil {
		t.Fatalf("Wait: %v", err)
	}

	select {
	case <-released:
	case <-time.After(time.Second):
		t.Fatal("the server still holds the stream a second after Wait returned")
	}
}

// TestABatchThatOnlyMovesTheCursor: a batch with no events but a cursor is
// progress past events a filter excluded. Wait keeps waiting and returns the
// advanced cursor when its window closes; Next skips it.
func TestABatchThatOnlyMovesTheCursor(t *testing.T) {
	t.Parallel()

	t.Run("Wait", func(t *testing.T) {
		t.Parallel()
		srv := watchServer(t, func(s stream) {
			s.open("41")
			s.frame("batch", `{"cursor":"42"}`)
			s.hold()
		})

		start := time.Now()
		out, err := mustClient(t, srv.URL).Wait(t.Context(), bud.WaitInput{TimeoutSeconds: 1})
		if err != nil {
			t.Fatalf("Wait: %v", err)
		}
		if !out.Empty() || out.Cursor != "42" {
			t.Errorf("out = %+v, want empty at the advanced cursor 42", out)
		}
		if elapsed := time.Since(start); elapsed < 900*time.Millisecond {
			t.Errorf("Wait returned after %v; an empty batch is not an answer to hand back", elapsed)
		}
	})

	t.Run("Next", func(t *testing.T) {
		t.Parallel()
		srv := watchServer(t, func(s stream) {
			s.open("41")
			s.frame("batch", `{"cursor":"42"}`)
			s.frame("batch", `{"cursor":"43","events":[{"id":"journal_01c"}]}`)
		})

		st, err := mustClient(t, srv.URL).Watch(t.Context(), bud.WaitInput{})
		if err != nil {
			t.Fatalf("Watch: %v", err)
		}
		defer func() { _ = st.Close() }()

		ev, ok := st.Next()
		if !ok || ev.ID != "journal_01c" {
			t.Fatalf("Next = %+v, %v; want the one event after the empty batch", ev, ok)
		}
		if _, ok := st.Next(); ok {
			t.Error("Next yielded a second event")
		}
	})
}

// TestALargeBatchArrivesWhole: a batch of many events whose subjects a stranger
// wrote in a script that escapes long runs well past a small frame bound, and a
// client that refused the frame would refuse it again from the same cursor for
// ever.
func TestALargeBatchArrivesWhole(t *testing.T) {
	t.Parallel()

	subject := strings.Repeat(`\u003c`, 120) // "<", escaped as the server sends it
	var events []string
	for i := range 100 {
		events = append(events, fmt.Sprintf(`{"id":"journal_%03d","type":"mail.received","data":{"subject":"%s","from":"%s"}}`,
			i, subject, subject))
	}
	batch := `{"cursor":"141","events":[` + strings.Join(events, ",") + `]}`
	if len(batch) < 128<<10 {
		t.Fatalf("the fixture is %d bytes; it must exceed the old 64 KiB bound to mean anything", len(batch))
	}

	srv := watchServer(t, func(s stream) {
		s.open("41")
		s.frame("batch", batch)
		s.hold()
	})

	out, err := mustClient(t, srv.URL).Wait(t.Context(), bud.WaitInput{})
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if len(out.Events) != 100 || out.Cursor != "141" {
		t.Errorf("got %d events at %q, want 100 at 141", len(out.Events), out.Cursor)
	}
}

// TestAnOversizedFrameIsMalformed: past the bound is a stream this package will
// not read, and it says so in the package's own terms rather than the parser's.
func TestAnOversizedFrameIsMalformed(t *testing.T) {
	t.Parallel()

	srv := watchServer(t, func(s stream) {
		s.open("41")
		s.frame("batch", `{"cursor":"42","pad":"`+strings.Repeat("x", 5<<20)+`"}`)
		s.hold()
	})

	_, err := mustClient(t, srv.URL).Wait(t.Context(), bud.WaitInput{TimeoutSeconds: 5})
	var e *bud.Error
	if !errors.As(err, &e) || e.Code != bud.CodeMalformedResponse || e.RequestID != "request_01stream" {
		t.Errorf("err = %v, want malformed_response naming the stream", err)
	}
}

// TestASlowHandshakeDoesNotSpendTheWindow: the window is for waiting on events,
// so a server slow to open the stream must not turn a quiet window into an error.
func TestASlowHandshakeDoesNotSpendTheWindow(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(1200 * time.Millisecond)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		s := stream{w: w, r: r}
		s.open("41")
		s.hold()
	}))
	t.Cleanup(srv.Close)

	out, err := mustClient(t, srv.URL).Wait(t.Context(), bud.WaitInput{TimeoutSeconds: 1})
	if err != nil {
		t.Fatalf("a slow handshake spent the window: %v", err)
	}
	if !out.Empty() || out.Cursor != "41" {
		t.Errorf("out = %+v", out)
	}
}

// flakyDoer fails the first n requests before any response, as a dropped
// connection does, then hands the rest to the real transport.
type flakyDoer struct {
	fail  atomic.Int64
	calls atomic.Int64
}

func (d *flakyDoer) Do(req *http.Request) (*http.Response, error) {
	d.calls.Add(1)
	if d.fail.Add(-1) >= 0 {
		return nil, errors.New("connection reset before any response")
	}
	return http.DefaultClient.Do(req)
}

// TestAHandshakeIsRetriedOnlyWhenTheConnectionFailed: opening a stream writes
// nothing, so a connection that failed before any response is retried under the
// client's policy. A refusal is an answer, and is not.
func TestAHandshakeIsRetriedOnlyWhenTheConnectionFailed(t *testing.T) {
	t.Parallel()

	client := func(t *testing.T, base string, d *flakyDoer) *bud.Client {
		t.Helper()
		c, err := bud.New("test-token", bud.WithBaseURL(base), bud.WithHTTPClient(d),
			bud.WithRetry(bud.TransientRetry{Attempts: 1, Backoff: []time.Duration{time.Millisecond}}))
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		return c
	}

	t.Run("a dropped connection", func(t *testing.T) {
		t.Parallel()
		srv := watchServer(t, func(s stream) {
			s.open("41")
			s.frame("batch", twoEvents)
			s.hold()
		})
		d := &flakyDoer{}
		d.fail.Store(1)

		out, err := client(t, srv.URL, d).Wait(t.Context(), bud.WaitInput{})
		if err != nil || len(out.Events) != 2 {
			t.Fatalf("Wait = %+v, %v; one dropped connection should be retried", out, err)
		}
		if n := d.calls.Load(); n != 2 {
			t.Errorf("%d attempts, want 2", n)
		}
	})

	t.Run("a connection that stays down", func(t *testing.T) {
		t.Parallel()
		d := &flakyDoer{}
		d.fail.Store(1 << 20)

		if _, err := client(t, "http://127.0.0.1:1", d).Wait(t.Context(), bud.WaitInput{}); err == nil {
			t.Fatal("Wait succeeded with no connection")
		}
		if n := d.calls.Load(); n != 2 {
			t.Errorf("%d attempts; the policy allows one retry, not more", n)
		}
	})

	t.Run("a refusal", func(t *testing.T) {
		t.Parallel()
		srv := refusingServer(t, http.StatusServiceUnavailable,
			`{"error":{"code":"upstream_unavailable","message":"a dependency did not answer"}}`)
		d := &flakyDoer{}

		out, err := client(t, srv.URL, d).Wait(t.Context(), bud.WaitInput{})
		if err != nil || out.Error == nil {
			t.Fatalf("Wait = %+v, %v; want the refusal as a value", out, err)
		}
		if n := d.calls.Load(); n != 1 {
			t.Errorf("%d attempts; a refusal is the caller's to retry, not this package's", n)
		}
	})
}

// TestTheCursorCoversOnlyWhatWasHandedOut: a consumer that stops part-way
// through a batch and resumes from Cursor must get the rest of that batch
// again, not skip it.
func TestTheCursorCoversOnlyWhatWasHandedOut(t *testing.T) {
	t.Parallel()

	srv := watchServer(t, func(s stream) {
		s.open("41")
		s.frame("batch", twoEvents)
		s.hold()
	})

	st, err := mustClient(t, srv.URL).Watch(t.Context(), bud.WaitInput{})
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}
	defer func() { _ = st.Close() }()

	if _, ok := st.Next(); !ok {
		t.Fatalf("Next: %v", st.Err())
	}
	if st.Cursor() != "41" {
		t.Errorf("Cursor = %q after one of two events; resuming there would skip the second", st.Cursor())
	}
	if _, ok := st.Next(); !ok {
		t.Fatalf("Next: %v", st.Err())
	}
	if st.Cursor() != "43" {
		t.Errorf("Cursor = %q after the whole batch, want 43", st.Cursor())
	}
}
