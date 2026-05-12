package semantik

import (
	"io"
	"strings"
)

// stringBody wraps a string as an io.ReadCloser for synthesizing
// http.Response bodies in tests.
func stringBody(s string) io.ReadCloser {
	return io.NopCloser(strings.NewReader(s))
}
