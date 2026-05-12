package semantik

import (
	"bytes"
	"fmt"
	"sync"

	gojson "github.com/goccy/go-json"
)

// bufPool holds reusable encoding buffers for request bodies. The pool
// pays off on Publish (vector-heavy bodies up to 2 MB) and Search
// (up to 1 MB); it is harmless for small bodies.
var bufPool = sync.Pool{
	New: func() any {
		b := make([]byte, 0, 4096)
		return &b
	},
}

// encodeJSON marshals v using goccy/go-json and returns a slice backed
// by a pooled buffer.
//
// Ownership rules — violating these silently corrupts later requests:
//
//   - body aliases the pooled buffer; the caller must not retain it
//     past the call to release.
//   - release must be invoked exactly once (typically via defer) after
//     the HTTP response body has been read and closed.
//   - The slice must not be handed to another goroutine that could
//     outlive the release call; the pool is shared across Clients.
//
// The marshal call is wrapped in a panic recovery to match the
// response-decode safety guarantee. goccy/go-json has been observed to
// panic on exotic struct shapes in addition to malformed decode input.
func encodeJSON(v any) (body []byte, release func(), err error) {
	bp := bufPool.Get().(*[]byte)
	*bp = (*bp)[:0]

	defer func() {
		if r := recover(); r != nil {
			*bp = (*bp)[:0]
			bufPool.Put(bp)
			body = nil
			release = func() {}
			err = fmt.Errorf("semantik: JSON encode panic: %v", r)
		}
	}()

	// Marshal into a bytes.Buffer that uses the pooled backing array.
	// We can't easily hand gojson.Encoder a *[]byte directly, so we wrap.
	buf := bytes.NewBuffer(*bp)
	enc := gojson.NewEncoder(buf)
	// gojson appends a trailing '\n' on Encode. Strip it to match
	// Marshal output — servers accept both but tests compare exact bytes.
	if err := enc.Encode(v); err != nil {
		*bp = buf.Bytes()
		bufPool.Put(bp)
		return nil, func() {}, err
	}
	b := buf.Bytes()
	if n := len(b); n > 0 && b[n-1] == '\n' {
		b = b[:n-1]
	}
	*bp = b

	return b, func() {
		// Cap the pooled buffer to avoid holding a giant publish body
		// in memory for the life of the pool.
		if cap(*bp) > 1<<20 {
			small := make([]byte, 0, 4096)
			*bp = small
		} else {
			*bp = (*bp)[:0]
		}
		bufPool.Put(bp)
	}, nil
}
