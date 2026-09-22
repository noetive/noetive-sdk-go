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

	"github.com/noetive/noetive-sdk-go/internal/sse"
)

// Watch: the journal as a stream, on one connection.
//
// # Why a stream when Wait exists
//
// Wait holds a poll for at most twenty-five seconds, and that bound is about the
// gateways it is served through rather than about the journal. A program holding
// its own connection has no such limit, so it stops paying a round trip per quiet
// interval.
//
// # Setup failure and in-flight failure are different problems
//
// The server sends a named `open` frame before anything else. Everything before it
// is setup: the handshake either completes or fails before any event has been
// delivered, so a setup failure is safe for this package to retry on the caller's
// behalf. Everything after it is in-flight, and this package will **not** silently
// reconnect — a fresh stream starts from a cursor, and events that arrived between
// the drop and the reconnect would be skipped without anybody noticing. That
// decision is the caller's, and [Stream.Cursor] is what it needs to make it.
//
// # Delivery
//
// At-least-once, against the journal's per-mailbox sequence. Dedupe on
// [JournalEvent.ID]. This package does not promise more, because the server does
// not.

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
// server that did not send its opening frame. Once Watch returns nil the stream is
// established and every later failure arrives through [Stream.Err].
func (c *Client) Watch(ctx context.Context, in WaitInput) (*Stream, error) {
	body, err := json.Marshal(in)
	if err != nil {
		return nil, preflight(CodeInvalid, "the request could not be encoded: %v", err)
	}

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
	if err != nil {
		return nil, err
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
			return nil, out.Error
		}
		return nil, errorFrom(resp.StatusCode, raw, requestID)
	}

	if ct := resp.Header.Get("Content-Type"); !isEventStream(ct) {
		_ = resp.Body.Close()
		return nil, &Error{
			Code: CodeMalformedResponse, HTTPStatus: resp.StatusCode, RequestID: requestID,
			Message: "the server answered 200 with content type " + ct + " rather than an event stream",
		}
	}

	s := &Stream{body: resp.Body, scanner: sse.NewScanner(resp.Body), requestID: requestID}

	// Read the handshake here, so that a server which never sends it is a setup
	// failure the caller can retry rather than a stream that yields nothing.
	if err := s.readOpen(); err != nil {
		_ = resp.Body.Close()
		return nil, err
	}
	return s, nil
}

// readOpen consumes the opening frame and records where the stream starts.
func (s *Stream) readOpen() error {
	if !s.scanner.Scan() {
		if err := s.scanner.Err(); err != nil {
			return err
		}
		return &Error{
			Code: CodeMalformedResponse, RequestID: s.requestID,
			Message: "the stream closed before its opening frame",
		}
	}

	frame := s.scanner.Frame()
	if frame.Event != eventOpen {
		return &Error{
			Code: CodeMalformedResponse, RequestID: s.requestID,
			Message: "the stream began with a " + quoted(frame.Event) + " frame rather than " + quoted(eventOpen),
		}
	}

	var open struct {
		Cursor string `json:"cursor"`
	}
	if err := json.Unmarshal([]byte(frame.Data), &open); err != nil {
		return &Error{
			Code: CodeMalformedResponse, RequestID: s.requestID,
			Message: fmt.Sprintf("the opening frame did not decode: %v", err),
		}
	}
	s.cursor = open.Cursor
	return nil
}

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
	for {
		if len(s.pending) > 0 {
			ev := s.pending[0]
			s.pending = s.pending[1:]
			return ev, true
		}
		if s.err != nil {
			return JournalEvent{}, false
		}

		if !s.scanner.Scan() {
			s.err = s.scanner.Err()
			return JournalEvent{}, false
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
				Code: CodeMalformedResponse, RequestID: s.requestID,
				Message: fmt.Sprintf("a stream frame did not decode: %v", err),
			}
			return JournalEvent{}, false
		}

		// A refusal mid-stream ends it. The server does not continue after one, and
		// a client that kept reading would look healthy while receiving nothing.
		if batch.Error != nil {
			if batch.Error.RequestID == "" {
				batch.Error.RequestID = s.requestID
			}
			s.err = batch.Error
			return JournalEvent{}, false
		}

		if batch.Cursor != "" {
			s.cursor = batch.Cursor
		}
		s.pending = batch.Events
	}
}

// Cursor is where the stream has read to.
//
// What a caller needs in order to reconnect deliberately. Pass it as
// [WaitInput.Cursor] on the next Watch — and know that events between the drop and
// the reconnect are not replayed, which is why this package does not reconnect on
// its own.
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
