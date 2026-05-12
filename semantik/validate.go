package semantik

import (
	"fmt"
	"math"
	"unicode/utf8"
)

// preflightErr returns an *Error representing a client-side rejection,
// i.e. HTTPStatus == 0 and Code == CodeInvalidRequest.
func preflightErr(format string, args ...any) *Error {
	return &Error{
		Code:    CodeInvalidRequest,
		Message: fmt.Sprintf(format, args...),
	}
}

// hasControlChar reports whether s contains any ASCII control character
// (0x00-0x1F or 0x7F), excluding nothing — keys and values are user-
// visible strings and should not carry these bytes. Matches the
// metadata acceptance rule the public API enforces.
func hasControlChar(s string) bool {
	for i := 0; i < len(s); i++ {
		b := s[i]
		if b < 0x20 || b == 0x7f {
			return true
		}
	}
	return false
}

// validateDimensions enforces 1 <= dim <= MaxVectorDim.
func validateDimensions(dim uint16) *Error {
	if dim == 0 {
		return preflightErr("dimensions must be greater than 0")
	}
	if dim > MaxVectorDim {
		return preflightErr("dimensions %d exceeds maximum %d", dim, MaxVectorDim)
	}
	return nil
}

// validateMetadata enforces the server's metadata constraints: no more
// than MaxMetadataKeys entries, each key <= MaxMetadataKeyLen and each
// value <= MaxMetadataValueLen, total key+value bytes <=
// MaxMetadataTotalBytes, and no control characters in keys or values.
func validateMetadata(md map[string]string) *Error {
	if len(md) == 0 {
		return nil
	}
	if len(md) > MaxMetadataKeys {
		return preflightErr("metadata has %d keys, maximum %d", len(md), MaxMetadataKeys)
	}
	total := 0
	for k, v := range md {
		if k == "" {
			return preflightErr("metadata key must not be empty")
		}
		if !utf8.ValidString(k) {
			return preflightErr("metadata key is not valid UTF-8")
		}
		if !utf8.ValidString(v) {
			return preflightErr("metadata value for key %q is not valid UTF-8", k)
		}
		if len(k) > MaxMetadataKeyLen {
			return preflightErr("metadata key %q exceeds %d bytes", k, MaxMetadataKeyLen)
		}
		if len(v) > MaxMetadataValueLen {
			return preflightErr("metadata value for key %q exceeds %d bytes", k, MaxMetadataValueLen)
		}
		if hasControlChar(k) {
			return preflightErr("metadata key %q contains control characters", k)
		}
		if hasControlChar(v) {
			return preflightErr("metadata value for key %q contains control characters", k)
		}
		total += len(k) + len(v)
	}
	if total > MaxMetadataTotalBytes {
		return preflightErr("metadata total size %d exceeds %d bytes", total, MaxMetadataTotalBytes)
	}
	return nil
}

// validatePublishItem enforces "at least one of text or vector",
// text byte cap, vector dim cap, and vector numeric sanity
// (no NaN / ±Inf, which would break server-side distance math).
// When both fields are populated the server's vector-wins precedence
// applies; the SDK does not reject this case.
func validatePublishItem(it PublishItem) *Error {
	hasText := it.Text != ""
	hasVec := len(it.Vector) > 0
	if !hasText && !hasVec {
		return preflightErr("publish item must have at least one of text or vector")
	}
	if hasText {
		if len(it.Text) > MaxTextBytes {
			return preflightErr("publish text exceeds %d bytes", MaxTextBytes)
		}
		if !utf8.ValidString(it.Text) {
			return preflightErr("publish text is not valid UTF-8")
		}
	}
	if hasVec {
		if len(it.Vector) > MaxVectorDim {
			return preflightErr("publish vector dim %d exceeds maximum %d", len(it.Vector), MaxVectorDim)
		}
		for i, f := range it.Vector {
			if math.IsNaN(float64(f)) || math.IsInf(float64(f), 0) {
				return preflightErr("publish vector index %d is NaN or Inf", i)
			}
		}
	}
	return nil
}
