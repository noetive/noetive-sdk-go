// Package sse parses Server-Sent Events streams in line with the W3C
// event-stream format used by Noetive Semantik's /v1/subscribe
// endpoint.
//
// The scanner is a small, allocation-conscious state machine over
// [bufio.Scanner]. It recognises the "event:" and "data:" directives,
// tolerates comment lines (leading ':'), and terminates a frame on an
// empty line. Unknown directives and empty frames are silently ignored.
//
// Per-frame size is capped, at [MaxFrameBytes] unless the caller chose a
// bound with [NewScannerLimit]; larger frames produce
// [ErrFrameTooLarge]. This bound protects SDK callers against
// unbounded memory growth on a misbehaving server or proxy.
package sse

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"strings"
)

// MaxFrameBytes is the largest single SSE frame (event + data lines) a
// [NewScanner] will accumulate before returning ErrFrameTooLarge.
// 64 KiB is an order of magnitude larger than any Semantik event; a
// stream whose frames batch many events sets its own bound with
// [NewScannerLimit].
const MaxFrameBytes = 64 * 1024

// initialBufSize is the starting line-buffer size handed to
// bufio.Scanner. 4 KiB is comfortably larger than any subscribed or
// match frame the server emits, while keeping the per-stream
// allocation small for callers that hold many idle subscriptions.
const initialBufSize = 4 * 1024

// ErrFrameTooLarge is returned by [Scanner.Scan] when a frame's
// accumulated event + data bytes exceed the scanner's bound.
var ErrFrameTooLarge = errors.New("sse: frame exceeds maximum size")

// Frame is one complete SSE event as delivered to [Scanner.Frame].
// Event is the value following "event:" (e.g. "subscribed", "match");
// Data is the concatenation of "data:" values within the frame joined
// by '\n' per the SSE specification.
//
// Field ordering: both strings are 16 B, order by convention.
type Frame struct {
	Event string
	Data  string
}

// Scanner reads SSE frames from an io.Reader. A Scanner is single-reader
// and not safe for concurrent use.
//
// Field ordering: pointer (8 B) > strings.Builder (24 B) > Frame (32 B).
type Scanner struct {
	sc   *bufio.Scanner
	data strings.Builder
	cur  Frame
	err  error
	size int
	max  int
}

// NewScanner returns a Scanner that reads from r with frames bounded by
// [MaxFrameBytes]. The caller is responsible for closing r (usually the
// HTTP response body).
func NewScanner(r io.Reader) *Scanner { return NewScannerLimit(r, MaxFrameBytes) }

// NewScannerLimit returns a Scanner whose frames are bounded by
// maxFrame bytes. The buffer grows only as frames need it, so a large
// bound costs memory only when a frame that large arrives.
func NewScannerLimit(r io.Reader, maxFrame int) *Scanner {
	if maxFrame < initialBufSize {
		panic("sse: a frame bound below the initial buffer size cannot hold a line")
	}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, initialBufSize), maxFrame)
	sc.Split(splitSSELines)
	return &Scanner{sc: sc, max: maxFrame}
}

// Scan reads the next frame. It returns true on success and false on
// EOF, an I/O error, or [ErrFrameTooLarge]. Call [Scanner.Frame] to
// access the parsed frame, and [Scanner.Err] to retrieve the
// terminating error (nil on clean EOF).
func (s *Scanner) Scan() bool {
	if s.err != nil {
		return false
	}
	s.cur = Frame{}
	s.data.Reset()
	s.size = 0

	for s.sc.Scan() {
		line := s.sc.Bytes()
		if len(line) == 0 {
			if s.cur.Event == "" && s.data.Len() == 0 {
				// Empty line between empty frames — skip.
				continue
			}
			return true
		}
		if line[0] == ':' {
			// Comment line — ignore.
			continue
		}
		s.size += len(line)
		if s.size > s.max {
			s.err = ErrFrameTooLarge
			return false
		}
		field, value := splitField(line)
		switch string(field) {
		case "event":
			// One string alloc from the underlying bytes — unavoidable.
			s.cur.Event = string(value)
		case "data":
			if s.data.Len() > 0 {
				s.data.WriteByte('\n')
			}
			s.data.Write(value)
		default:
			// Unknown field — ignore per the SSE spec.
		}
	}
	if err := s.sc.Err(); err != nil {
		if errors.Is(err, bufio.ErrTooLong) {
			s.err = ErrFrameTooLarge
			return false
		}
		s.err = err
		return false
	}
	// Clean EOF. Emit any partially-accumulated frame.
	if s.cur.Event != "" || s.data.Len() > 0 {
		return true
	}
	s.err = io.EOF
	return false
}

// Frame returns the most recently parsed frame. Valid only after a
// call to Scan returned true.
func (s *Scanner) Frame() Frame {
	s.cur.Data = s.data.String()
	return s.cur
}

// Err returns the first non-EOF error encountered during scanning.
// Returns nil on clean EOF.
func (s *Scanner) Err() error {
	if s.err == io.EOF {
		return nil
	}
	return s.err
}

// splitField separates an SSE "field: value" line. Per the spec, a
// single space after ':' is stripped but additional spaces are
// preserved. A line without ':' treats the entire line as the field
// name and uses an empty value. The returned slices alias line.
func splitField(line []byte) (field, value []byte) {
	field, value, ok := bytes.Cut(line, []byte{':'})
	if !ok {
		return line, nil
	}
	if len(value) > 0 && value[0] == ' ' {
		value = value[1:]
	}
	return field, value
}

// splitSSELines is a [bufio.SplitFunc] that emits one line per call,
// terminated by '\n', '\r\n', or '\r'. The returned token excludes the
// terminator. Needed because [bufio.ScanLines] does not treat bare '\r'
// (used by some SSE implementations) as a line break.
func splitSSELines(data []byte, atEOF bool) (advance int, token []byte, err error) {
	if atEOF && len(data) == 0 {
		return 0, nil, nil
	}
	for i, b := range data {
		switch b {
		case '\n':
			return i + 1, data[:i], nil
		case '\r':
			if i+1 < len(data) && data[i+1] == '\n' {
				return i + 2, data[:i], nil
			}
			if i+1 < len(data) || atEOF {
				return i + 1, data[:i], nil
			}
			// Defer: need more bytes to know if \r\n or lone \r.
			return 0, nil, nil
		}
	}
	if atEOF {
		return len(data), data, nil
	}
	return 0, nil, nil
}
