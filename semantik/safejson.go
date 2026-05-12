package semantik

import (
	"fmt"
	"io"

	gojson "github.com/goccy/go-json"
)

// safeUnmarshal decodes data into v. It recovers from panics in the
// underlying JSON library so that a malicious or corrupt response body
// cannot crash the caller. See SECURITY.md for the rationale.
//
// A recovered panic is reported as a formatted error; callers cannot
// distinguish panic recovery from ordinary decode failures. The
// returned error is non-nil on failure and nil on success.
//
// Caveat under -race: Go's checkptr sanitiser can convert an unsafe
// pointer misuse inside goccy/go-json into a runtime.throw that
// recover() cannot intercept. Fuzzers that exercise crafted JSON
// should therefore NOT be run with -race; unit tests are safe because
// their inputs are well-formed. If this becomes operationally
// painful, swap gojson for [encoding/json] in this package — the
// safety wrapper is the only thing standing between a crafted payload
// and the caller's process.
func safeUnmarshal(data []byte, v any) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("semantik: JSON decode panic: %v", r)
		}
	}()
	return gojson.Unmarshal(data, v)
}

// safeDecode drains r (capped) and delegates to [safeUnmarshal]. The
// cap matches [decodeError]: 64 KiB is larger than any legitimate
// Semantik response body that the SDK needs to parse (errors,
// PublishResponse, SearchResponse with a reasonable result set).
func safeDecode(r io.Reader, v any) error {
	data, err := io.ReadAll(io.LimitReader(r, maxResponseBytes))
	if err != nil {
		return err
	}
	return safeUnmarshal(data, v)
}

// maxResponseBytes caps response body reads. Aligned with
// [MaxSearchBodyBytes] so the decode cap tracks the request-body cap
// on the wire — the two are the same by design.
const maxResponseBytes = MaxSearchBodyBytes
