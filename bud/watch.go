package bud

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"go.noetive.io/noetive-sdk-go/internal/sse"
)

// Watch and Wait: the journal as a stream, and one bounded window of it.
//
// # One endpoint, two ways to hold it
//
// The server serves the journal only as a stream. Watch hands that stream to a
// caller that will hold it for as long as it runs. Wait opens the same stream for
// one bounded window and returns the first batch, for a caller that thinks in
// turns — an agent loop, a cron job — and would otherwise have to manage a
// connection it does not want.
//
// # Setup failure and in-flight failure are different problems
//
// The server sends a named `open` frame before anything else. Everything before it
// is setup: the handshake either completes or fails before any event has been
// delivered, so a connection that fails before any response is retried here under
// the client's [RetryPolicy]. Everything after it is in-flight, and this package
// does not reconnect on its own. Nothing is lost by that: the journal is stored,
// so a new Watch from [Stream.Cursor] replays everything after it. Whether and
// when to reconnect is the caller's budget to spend.
//
// # Delivery
//
// At-least-once, against the journal's per-mailbox sequence. A reconnect from a
// cursor can repeat events, so dedupe on [JournalEvent.ID]. This package does not
// promise more, because the server does not.

// Stream is an open connection to the journal.
//
// Not safe for concurrent use: one goroutine reads it. Close it when done, or
// cancel the context the call was made with.
type Stream struct {
	body    io.ReadCloser
	scanner *sse.Scanner

	cursor  string
	pending []JournalEvent
	err     error

	requestID string
}

// Watch opens the stream.
//
// The returned error is a setup failure: the connection, the credential, or a
// server that did not send its opening frame. A refusal before the stream opens
// arrives as the *Error the server sent. Once Watch returns nil the stream is
// established and every later failure arrives through [Stream.Err].
func (c *Client) Watch(ctx context.Context, in WaitInput) (*Stream, error) {
	s, refusal, err := c.open(ctx, in)
	if err != nil {
		return nil, err
	}
	if refusal != nil {
		return nil, refusal
	}
	return s, nil
}

// Wait blocks until something happens, or until the window closes.
//
// The window is [WaitInput.TimeoutSeconds], at most [MaxWaitSeconds], which is
// also the default. It starts once the stream is open, so a slow handshake does
// not eat into it; the handshake itself is bounded by the caller's context and,
// on the default transport, by [DefaultResponseTimeout]. Within the window Wait
// returns the first batch carrying events, whole, with the cursor after it; pass
// that cursor to the next Wait.
//
// An empty result is success: see [WaitOutput.Empty]. It carries the latest
// cursor the stream gave, so the next Wait resumes where this one stopped.
//
// A refusal is a value, as on the unary operations: one before the stream opens
// and one the server sends in flight both come back as [WaitOutput.Error] with a
// nil error. A non-nil error is a connection that failed, a stream this package
// could not read (an *Error with CodeMalformedResponse), a stream that closed
// before answering, or the caller's own context ending.
//
// With Types or AgentPart set, an empty Wait returns the cursor it started
// from, and the next Wait reads past the same excluded events again. A filter
// over a busy mailbox can therefore stop making progress; filter on the
// caller's side when the excluded traffic is heavy.
//
// For a long-lived consumer prefer [Client.Watch], which holds one connection
// instead of opening one per window.
func (c *Client) Wait(ctx context.Context, in WaitInput) (WaitOutput, error) {
	stream, cancel := context.WithCancel(ctx)
	defer cancel()

	s, refusal, err := c.open(stream, in)
	if err != nil {
		return WaitOutput{}, err
	}
	if refusal != nil {
		// The caller's own cursor, so a refusal it retries after fixing the
		// request resumes from the same place.
		return WaitOutput{Cursor: in.Cursor, Error: refusal}, nil
	}
	defer func() { _ = s.Close() }()

	// Closing the window cancels the request, which ends the read in progress.
	// The flag says it was the window and not the caller.
	var closed atomic.Bool
	window := time.AfterFunc(waitWindow(in.TimeoutSeconds), func() {
		closed.Store(true)
		cancel()
	})
	defer window.Stop()

	for {
		batch, ok := s.nextBatch()
		switch {
		case ok && len(batch.Events) == 0:
			// A batch that only moves the cursor. Nothing to hand back yet;
			// the stream has recorded the position.
			continue
		case ok, batch.Error != nil:
			return batch, nil
		case ctx.Err() != nil:
			return WaitOutput{}, ctx.Err()
		case closed.Load():
			// The window closed with nothing in it. Not a failure.
			return WaitOutput{Cursor: s.Cursor()}, nil
		case s.Err() != nil:
			return WaitOutput{}, s.Err()
		default:
			// The server ended the stream without a batch, which it does only
			// when it is going away. Reporting that as an empty window would
			// send the caller straight back to a server that is not there.
			return WaitOutput{}, io.ErrUnexpectedEOF
		}
	}
}

// waitWindow is how long Wait holds the stream.
func waitWindow(seconds int) time.Duration {
	if seconds <= 0 || seconds > MaxWaitSeconds {
		seconds = MaxWaitSeconds
	}
	return time.Duration(seconds) * time.Second
}

// open performs the handshake.
//
// A refusal the server sent before the stream opened is returned on its own,
// apart from the error, so Watch can return it as an error and Wait as a value.
func (c *Client) open(ctx context.Context, in WaitInput) (*Stream, *Error, error) {
	body, err := json.Marshal(in)
	if err != nil {
		return nil, nil, preflight(CodeInvalid, "the request could not be encoded: %v", err)
	}

	resp, err := c.connect(ctx, in, body)
	if err != nil {
		return nil, nil, err
	}
	requestID := resp.Header.Get("X-Request-Id")

	// A refusal arrives as the envelope at a failing status, not as a stream.
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		_ = resp.Body.Close()

		var out WaitOutput
		if json.Unmarshal(raw, &out) == nil && out.Error != nil {
			out.Error.HTTPStatus = resp.StatusCode
			if out.Error.RequestID == "" {
				out.Error.RequestID = requestID
			}
			return nil, out.Error, nil
		}
		return nil, nil, errorFrom(resp.StatusCode, raw, requestID)
	}

	if ct := resp.Header.Get("Content-Type"); !isEventStream(ct) {
		_ = resp.Body.Close()
		return nil, nil, &Error{
			Code: CodeMalformedResponse, HTTPStatus: resp.StatusCode, RequestID: requestID,
			Message: "the server answered 200 with content type " + ct + " rather than an event stream",
		}
	}

	s := &Stream{body: resp.Body, scanner: sse.NewScannerLimit(resp.Body, maxFrameBytes), requestID: requestID}

	// Read the handshake here, so that a server which never sends it is a setup
	// failure the caller can retry rather than a stream that yields nothing.
	if err := s.readOpen(); err != nil {
		_ = resp.Body.Close()
		return nil, nil, err
	}
	return s, nil, nil
}

// connect sends the handshake request, retrying under the client's policy when
// the connection fails before any response arrives.
//
// Only then: a refusal or a stream that does not begin properly is an answer,
// and retrying it would hide a server that disagrees with this package.
func (c *Client) connect(ctx context.Context, in WaitInput, body []byte) (*http.Response, error) {
	for attempt := 0; ; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/v1/watch",
			bytes.NewReader(body))
		if err != nil {
			return nil, preflight(CodeInvalid, "the request could not be built: %v", err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "text/event-stream")
		req.Header.Set("User-Agent", UserAgent())
		if header := c.credential(ctx); header != "" {
			req.Header.Set("Authorization", header)
		}

		resp, err := c.doer.Do(req)
		if err == nil {
			return resp, nil
		}
		if errors.Is(err, errRedirectRefused) || !c.mayRetry(attempt, "watch", in) {
			return nil, err
		}
		if waitErr := c.retry.Wait(ctx, attempt); waitErr != nil {
			return nil, err
		}
	}
}

// readOpen consumes the opening frame and records where the stream starts.
func (s *Stream) readOpen() error {
	if !s.scanner.Scan() {
		if err := s.scanner.Err(); err != nil {
			return err
		}
		return &Error{
			Code: CodeMalformedResponse, HTTPStatus: http.StatusOK, RequestID: s.requestID,
			Message: "the stream closed before its opening frame",
		}
	}

	frame := s.scanner.Frame()
	if frame.Event != eventOpen {
		return &Error{
			Code: CodeMalformedResponse, HTTPStatus: http.StatusOK, RequestID: s.requestID,
			Message: "the stream began with a " + quoted(frame.Event) + " frame rather than " + quoted(eventOpen),
		}
	}

	var open struct {
		Cursor string `json:"cursor"`
	}
	if err := json.Unmarshal([]byte(frame.Data), &open); err != nil {
		return &Error{
			Code: CodeMalformedResponse, HTTPStatus: http.StatusOK, RequestID: s.requestID,
			Message: fmt.Sprintf("the opening frame did not decode: %v", err),
		}
	}
	s.cursor = open.Cursor
	return nil
}

// maxFrameBytes bounds one frame on the stream.
//
// A batch can carry many events, each with a subject and an address a stranger
// chose, and escaping can multiply their size. This leaves ample room for that,
// and is bounded, so a misbehaving proxy cannot decide how much one stream
// holds.
const maxFrameBytes = 4 << 20

// The frame names the server sends. Only these two carry data; a comment line is
// a heartbeat and the scanner drops it.
const (
	eventOpen  = "open"
	eventBatch = "batch"
)

// Next returns the next event, blocking until one arrives.
//
// It returns false at the end of the stream and on any failure; check [Stream.Err]
// to tell a clean close from a drop. A heartbeat on a quiet stream is consumed
// here and never surfaces — it exists to prove the connection is alive, which is
// not news the caller has to act on.
func (s *Stream) Next() (JournalEvent, bool) {
	for len(s.pending) == 0 {
		batch, ok := s.nextBatch()
		if !ok {
			return JournalEvent{}, false
		}
		s.pending = batch.Events
	}
	ev := s.pending[0]
	s.pending = s.pending[1:]
	return ev, true
}

// nextBatch returns the next batch frame whole.
//
// False at the end of the stream and on any failure, with the reason in s.err.
// A refusal the server sent in flight also comes back as the batch carrying it,
// so Wait can return it as a value. Cursor is always the stream's latest, because
// the server may send a refusal without one.
func (s *Stream) nextBatch() (WaitOutput, bool) {
	for {
		if s.err != nil {
			return WaitOutput{}, false
		}
		if !s.scanner.Scan() {
			s.err = s.scanner.Err()
			if errors.Is(s.err, sse.ErrFrameTooLarge) {
				s.err = &Error{
					Code: CodeMalformedResponse, HTTPStatus: http.StatusOK, RequestID: s.requestID,
					Message: fmt.Sprintf("a stream frame exceeded %d bytes", maxFrameBytes),
				}
			}
			return WaitOutput{}, false
		}

		frame := s.scanner.Frame()
		if frame.Event != eventBatch {
			// An unknown frame is skipped rather than fatal. The server may name a
			// frame this version does not know, and refusing to read the rest of
			// the stream over it would be a client that breaks on a server that
			// grew.
			continue
		}

		var batch WaitOutput
		if err := json.Unmarshal([]byte(frame.Data), &batch); err != nil {
			s.err = &Error{
				Code: CodeMalformedResponse, HTTPStatus: http.StatusOK, RequestID: s.requestID,
				Message: fmt.Sprintf("a stream frame did not decode: %v", err),
			}
			return WaitOutput{}, false
		}

		if batch.Cursor != "" {
			s.cursor = batch.Cursor
		}
		batch.Cursor = s.cursor

		// A refusal mid-stream ends it. The server does not continue after one, and
		// a client that kept reading would look healthy while receiving nothing.
		if batch.Error != nil {
			// The stream answered 200; the refusal came on it, not before sending.
			batch.Error.HTTPStatus = http.StatusOK
			if batch.Error.RequestID == "" {
				batch.Error.RequestID = s.requestID
			}
			s.err = batch.Error
			return batch, false
		}
		return batch, true
	}
}

// Cursor is where the stream has read to.
//
// What a caller needs in order to reconnect. Pass it as [WaitInput.Cursor] on the
// next Watch or Wait; the journal replays everything after it, including what
// arrived while no stream was open.
func (s *Stream) Cursor() string { return s.cursor }

// Err is why the stream ended, or nil for a clean close.
func (s *Stream) Err() error {
	if errors.Is(s.err, io.EOF) {
		return nil
	}
	return s.err
}

// RequestID correlates this stream with the server's own records.
func (s *Stream) RequestID() string { return s.requestID }

// Close releases the connection.
func (s *Stream) Close() error { return s.body.Close() }

// isEventStream accepts the media type with or without parameters.
func isEventStream(contentType string) bool {
	media, _, _ := strings.Cut(contentType, ";")
	return strings.TrimSpace(strings.ToLower(media)) == "text/event-stream"
}

// quoted renders a frame name for a message without letting an empty one read as
// a missing word.
func quoted(s string) string {
	if s == "" {
		return "(unnamed)"
	}
	return `"` + s + `"`
}
