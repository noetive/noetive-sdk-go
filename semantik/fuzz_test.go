package semantik

import (
	"bytes"
	"io"
	"math"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// FuzzErrorDecode feeds arbitrary bytes into decodeError across the
// full status-code space and a fuzzed Retry-After header. It asserts
// the decoder never panics, always returns a non-nil *Error with the
// status preserved, never invents a RetryAfter exceeding the SDK's
// own cap, and never produces a stringified form that panics.
func FuzzErrorDecode(f *testing.F) {
	type seed struct {
		status     uint16
		retryAfter string
		body       string
	}
	seeds := []seed{
		{429, "", ""},
		{429, "", "{}"},
		{429, "", `{"error":"backpressure","retry_after_ms":100}`},
		{503, "30", `{"error":"unavailable"}`},
		{503, "Wed, 21 Oct 2099 07:28:00 GMT", ""},
		{500, "", `{"error":""}`},
		{400, "", `{"error":"x","message":"y","retry_after_ms":1}`},
		{500, "", `{"error":"internal_error","retry_after_ms":4294967295}`},
		{502, "", `<html>gateway timeout</html>`},
		{200, "", `null`},
		{418, "", `"just a string"`},
		{401, "", `{"error":42}`},
		{402, "", `{"error":"a","message":null}`},
		{0, "abc", `{` + strings.Repeat(`"x":1,`, 1000) + `"e":"f"}`},
		{65535, "-1", "{}"},
	}
	for _, s := range seeds {
		f.Add(s.status, s.retryAfter, []byte(s.body))
	}

	f.Fuzz(func(t *testing.T, status uint16, retryAfter string, body []byte) {
		h := http.Header{}
		if retryAfter != "" {
			h.Set("Retry-After", retryAfter)
		}
		resp := &http.Response{
			StatusCode: int(status),
			Body:       io.NopCloser(strings.NewReader(string(body))),
			Header:     h,
		}
		defer resp.Body.Close()
		e := decodeError(resp)
		if e == nil {
			t.Fatal("decodeError returned nil")
		}
		if e.HTTPStatus != int(status) {
			t.Errorf("HTTPStatus = %d, want %d", e.HTTPStatus, status)
		}
		if e.Code == "" {
			t.Error("Code must not be empty")
		}
		// RetryAfter must always be non-negative.
		if e.RetryAfter < 0 {
			t.Errorf("RetryAfter must be non-negative, got %v", e.RetryAfter)
		}
		// When no Retry-After header is supplied, the value can only
		// come from the body path (retry_after_ms), which is capped at
		// maxRetryAfterMs (1 hour) as defence-in-depth.
		if retryAfter == "" {
			bodyCap := time.Duration(maxRetryAfterMs) * time.Millisecond
			if e.RetryAfter > bodyCap {
				t.Errorf("body-derived RetryAfter %v exceeds 1-hour cap %v",
					e.RetryAfter, bodyCap)
			}
		}
		_ = e.Error() // assert String formatting does not panic.
	})
}

// FuzzRequestEncode feeds arbitrary bytes into every public request
// type via JSON unmarshaling (through the panic-safe wrapper) and
// asserts the encode-idempotence property: encoding the decoded
// struct and re-decoding must yield bytes equal to the first encode.
// This is the wire-equivalence form of round-trip — strictly weaker
// than struct DeepEqual but the right shape for types with
// `omitempty` containers, where nil and empty diverge across one
// round but converge after a normalising pass.
func FuzzRequestEncode(f *testing.F) {
	seeds := []string{
		`{"query":"q","namespace":"n","model":"m","dimensions":3}`,
		`{"namespace":"n","model":"m","dimensions":3,"items":[{"text":"x"}]}`,
		`{"namespace":"n","model":"m","dimensions":3,"items":[{"vector":[0.1,0.2]}]}`,
		`{"namespace":"n","model":"m","dimensions":3,"items":[{"text":"x"}],"idempotency_key":"k","ack":"durable","metadata":{"a":"b"}}`,
		`{"namespace":"n","model":"m","dimensions":3,"items":[{"vector":[1,2,3,4,5,6,7,8,9,10]}],"ack":"stored"}`,
		`{"query":"q","cursor":0}`,
		`{"query":"MATCH DISTANCE(\"x\") WITHIN 0.4","cursor":5}`,
		`{"query":"q","namespace":"n","model":"m","dimensions":3,"limit":42}`,
		`{"query":"sub query","namespace":"n","model":"m","dimensions":3}`,
	}
	for _, s := range seeds {
		f.Add([]byte(s))
	}

	f.Fuzz(func(t *testing.T, raw []byte) {
		encodeIdempotent(t, raw, new(SearchRequest))
		encodeIdempotent(t, raw, new(PublishRequest))
		encodeIdempotent(t, raw, new(LintRequest))
		encodeIdempotent(t, raw, new(SubscribeRequest))
	})
}

// encodeIdempotent decodes raw, encodes the result, decodes that, and
// asserts encoding the second decode yields bytes equal to the first
// encode. It is the wire-equivalence form of the round-trip property:
// strictly weaker than struct DeepEqual but the right shape for SDK
// types that carry `omitempty` containers, where nil and empty
// diverge across one round but converge after a normalising pass —
// encode bytes do not.
//
// Used for every public request and response type. Catches encode/
// decode asymmetries and shape drift that would corrupt the wire.
func encodeIdempotent[T any](t *testing.T, raw []byte, first *T) {
	t.Helper()
	if err := safeUnmarshal(raw, first); err != nil {
		return
	}
	if !allStringsValidUTF8(first) {
		return
	}
	body1, release1, err := encodeJSON(*first)
	if err != nil {
		return
	}
	// Copy because release1 returns the buffer to the pool.
	body1Copy := append([]byte(nil), body1...)
	release1()
	second := new(T)
	if err := safeUnmarshal(body1Copy, second); err != nil {
		t.Fatalf("re-decode of SDK-encoded %T failed: %v", first, err)
	}
	body2, release2, err := encodeJSON(*second)
	if err != nil {
		t.Fatalf("second encode of %T failed: %v", first, err)
	}
	defer release2()
	if !bytes.Equal(body1Copy, body2) {
		t.Fatalf("encode not idempotent for %T\nfirst encode : %s\nsecond encode: %s",
			first, body1Copy, body2)
	}
}

// allStringsValidUTF8 walks v via reflection and returns false if any
// reachable string field carries bytes that are not valid UTF-8.
// Used by the round-trip fuzzer to scope the property to inputs the
// SDK's preflight would actually accept.
func allStringsValidUTF8(v any) bool {
	return walkStringsValid(reflect.ValueOf(v))
}

func walkStringsValid(v reflect.Value) bool {
	switch v.Kind() {
	case reflect.Pointer, reflect.Interface:
		if v.IsNil() {
			return true
		}
		return walkStringsValid(v.Elem())
	case reflect.Struct:
		for i := 0; i < v.NumField(); i++ {
			if !walkStringsValid(v.Field(i)) {
				return false
			}
		}
		return true
	case reflect.Slice, reflect.Array:
		for i := 0; i < v.Len(); i++ {
			if !walkStringsValid(v.Index(i)) {
				return false
			}
		}
		return true
	case reflect.Map:
		iter := v.MapRange()
		for iter.Next() {
			if !walkStringsValid(iter.Key()) || !walkStringsValid(iter.Value()) {
				return false
			}
		}
		return true
	case reflect.String:
		return utf8.ValidString(v.String())
	default:
		return true
	}
}

// FuzzMetadataValidation drives validateMetadata against multi-entry
// maps so MaxMetadataKeys and MaxMetadataTotalBytes are reachable
// alongside the per-key bounds. Two properties are asserted:
//
//   - One-way: an input that exceeds any individual server-side bound
//     (empty key, oversize key/value, control chars, invalid UTF-8)
//     must be rejected.
//   - Reverse: if validation accepted the map, every per-key
//     invariant — entry count, total bytes, key non-empty, key+value
//     length caps, valid UTF-8, no control chars — must hold.
func FuzzMetadataValidation(f *testing.F) {
	f.Add("author", "jdoe", "topic", "ml")
	f.Add("", "v", "k2", "v2")
	f.Add("k", "", "k2", "v2")
	f.Add("x", strings.Repeat("a", MaxMetadataValueLen+1), "k2", "v2")
	f.Add(strings.Repeat("a", MaxMetadataKeyLen+1), "v", "k2", "v2")
	f.Add("k\nbroken", "v", "k2", "v2")
	f.Add("k", "v\x00", "k2", "v2")
	f.Add("k", "v", "k2", "v2") // happy path
	// Push toward MaxMetadataTotalBytes via the value side.
	half := MaxMetadataTotalBytes / 2
	f.Add("k1", strings.Repeat("a", half), "k2", strings.Repeat("b", half))
	// Invalid UTF-8 in key and in value.
	f.Add("\xff", "v", "k2", "v2")
	f.Add("k", "\xc3", "k2", "v2")

	f.Fuzz(func(t *testing.T, k1, v1, k2, v2 string) {
		md := map[string]string{k1: v1, k2: v2}
		err := validateMetadata(md)

		// Forward: rejection conditions that must trip the validator.
		anyEmptyKey := false
		anyOverKey := false
		anyOverVal := false
		anyControl := false
		anyInvalidUTF8 := false
		total := 0
		for k, v := range md {
			total += len(k) + len(v)
			if k == "" {
				anyEmptyKey = true
			}
			if len(k) > MaxMetadataKeyLen {
				anyOverKey = true
			}
			if len(v) > MaxMetadataValueLen {
				anyOverVal = true
			}
			if hasControlChar(k) || hasControlChar(v) {
				anyControl = true
			}
			if !utf8.ValidString(k) || !utf8.ValidString(v) {
				anyInvalidUTF8 = true
			}
		}
		mustFail := anyEmptyKey || anyOverKey || anyOverVal ||
			anyControl || anyInvalidUTF8 ||
			len(md) > MaxMetadataKeys || total > MaxMetadataTotalBytes
		if mustFail && err == nil {
			t.Fatalf("expected rejection but accepted: md=%#v", md)
		}

		// Reverse: an accepted map satisfies every invariant.
		if err == nil {
			if len(md) > MaxMetadataKeys {
				t.Fatalf("accepted map with %d keys > %d", len(md), MaxMetadataKeys)
			}
			if total > MaxMetadataTotalBytes {
				t.Fatalf("accepted map with total %d bytes > %d", total, MaxMetadataTotalBytes)
			}
			for k, v := range md {
				if k == "" {
					t.Fatalf("accepted map with empty key")
				}
				if len(k) > MaxMetadataKeyLen || len(v) > MaxMetadataValueLen {
					t.Fatalf("accepted oversize entry %q=%q", k, v)
				}
				if hasControlChar(k) || hasControlChar(v) {
					t.Fatalf("accepted control chars in %q=%q", k, v)
				}
				if !utf8.ValidString(k) || !utf8.ValidString(v) {
					t.Fatalf("accepted invalid UTF-8 in %q=%q", k, v)
				}
			}
		}
	})
}

// FuzzRetryAfterHeader exercises the header parser against every
// shape an intermediary or server could legally emit (and plenty it
// couldn't). Properties:
//
//   - never panic
//   - never return a negative duration (the parser's only documented
//     contract on out-of-band values)
//
// The header path is intentionally permissive: an HTTP-date 200+
// years out can yield a duration close to time.Duration's int64 max.
// The only hard contract is "non-negative and overflow-safe"; the
// 1-hour body cap is defence-in-depth applied in decodeError, not
// here.
func FuzzRetryAfterHeader(f *testing.F) {
	seeds := []string{
		"",
		"0",
		"1",
		"30",
		"   30   ",
		"-10",
		"abc",
		"Wed, 21 Oct 2099 07:28:00 GMT",
		"Wed, 21 Oct 2000 07:28:00 GMT", // far past
		"not a date",
		"1s",
		"999999999999999999999999999999999", // overflows int
		"\x00",
		"2\n",
		"text/event-stream",  // absurd but possible from a bad proxy
		"9999999999",         // ten billion seconds — wraps duration math?
		"-9223372036854775808", // int64 min
		"Sun, 06 Nov 1994 08:49:37 GMT", // RFC 850-style past
		"Sun Nov  6 08:49:37 1994",      // ANSI C asctime past
	}
	for _, s := range seeds {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, v string) {
		h := http.Header{}
		if v != "" {
			h.Set("Retry-After", v)
		}
		resp := &http.Response{Header: h}
		d := retryAfterFromHeader(resp)
		if d < 0 {
			t.Errorf("negative duration %v for %q", d, v)
		}
	})
}

// FuzzContentType exercises isEventStreamContentType against arbitrary
// byte strings. Properties:
//
//   - never panic
//   - true ⇒ after stripping parameters and whitespace, the string
//     equals "text/event-stream" case-insensitively
//   - symmetric on outer whitespace: padding the input with spaces
//     or tabs does not change the verdict
func FuzzContentType(f *testing.F) {
	seeds := []string{
		"",
		"text/event-stream",
		"text/event-stream; charset=utf-8",
		"TEXT/EVENT-STREAM",
		"text/event-stream ;",
		"  text/event-stream  ",
		"application/json",
		"text/event-stream\n",
		"text/event-stream;boundary=\"abc\"",
		";text/event-stream",
		"\x00text/event-stream",
		"\ttext/event-stream\t",
		"text/event-stream;;",
		"Text/Event-Stream;Charset=UTF-8",
	}
	for _, s := range seeds {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, ct string) {
		got := isEventStreamContentType(ct)
		if got {
			// Invariant: true ⇒ after trimming params and whitespace,
			// the string equals "text/event-stream" case-insensitively.
			main := ct
			if i := strings.IndexByte(main, ';'); i >= 0 {
				main = main[:i]
			}
			main = strings.TrimSpace(main)
			if !strings.EqualFold(main, "text/event-stream") {
				t.Errorf("returned true for %q but core type is %q", ct, main)
			}
		}
		// Symmetry: outer whitespace must not flip the verdict.
		padded := " \t" + ct + "\t "
		if isEventStreamContentType(padded) != got {
			t.Errorf("verdict changed under whitespace padding: %q got %v, padded %q got %v",
				ct, got, padded, !got)
		}
	})
}

// FuzzSearchResponseDecode feeds arbitrary bytes through the SDK's
// happy-path decode wrapper into SearchResponse. Two properties:
// never panic, and decode→encode→decode round-trips for any input
// the decoder accepted (catches lossy json tags, struct-shape drift).
// FuzzErrorDecode already covers the 4xx/5xx body shape; this fuzzer
// focuses on the 2xx payload decoder.
func FuzzSearchResponseDecode(f *testing.F) {
	seeds := []string{
		`{}`,
		`{"results":[]}`,
		`{"results":[{"message_id":"m1","score":0.9}]}`,
		`{"results":[{"message_id":"m","score":0.5,"metadata":{"k":"v"}}]}`,
		`{"results":[{"message_id":"m","namespace":"global","content":"x","score":0.5}]}`,
		`{"results":null}`,
		`{"results":[{"score":"not-a-number"}]}`,
	}
	for _, s := range seeds {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, body []byte) {
		encodeIdempotent(t, body, new(SearchResponse))
	})
}

// FuzzPublishResponseDecode covers the PublishResponse decoder path.
// Asserts non-panic plus the decode/encode round-trip property; the
// PublishResponse fields (string + two uint64s) are simple enough
// that any drift here is a likely shape regression.
func FuzzPublishResponseDecode(f *testing.F) {
	seeds := []string{
		`{}`,
		`{"message_id":"m","epoch":1,"seq":2}`,
		`{"message_id":null}`,
		`{"epoch":"not-a-number"}`,
		`{"seq":9999999999999999999999999999}`,
		`{"message_id":"m","epoch":18446744073709551615,"seq":18446744073709551615}`, // uint64 max
	}
	for _, s := range seeds {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, body []byte) {
		encodeIdempotent(t, body, new(PublishResponse))
	})
}

// FuzzLintResponseDecode covers the LintResponse decoder path. Lint
// responses carry nested arrays of diagnostic and completion objects
// that exercise a larger portion of the struct decoder than a plain
// PublishResponse. Same non-panic + round-trip property.
func FuzzLintResponseDecode(f *testing.F) {
	seeds := []string{
		`{"valid":true,"normalized":"x"}`,
		`{"valid":false,"diagnostics":[{"severity":"error","line":1}]}`,
		`{"completions":[{"label":"MATCH","kind":"keyword"}]}`,
		`{"diagnostics":null,"completions":null}`,
		`{"valid":true,"diagnostics":[{"severity":"warn","message":"x","line":1,"col":2,"end_line":3,"end_col":4}],"completions":[{"label":"L","kind":"K","detail":"D"}]}`,
	}
	for _, s := range seeds {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, body []byte) {
		encodeIdempotent(t, body, new(LintResponse))
	})
}

// FuzzValidatePublishItem exercises the at-least-one-of + size +
// UTF-8 + NaN/Inf guardrails. Properties:
//
//   - never panic
//   - at-least-one-of: an item with neither text nor vector must error;
//     items with both are accepted (server applies vector-wins precedence)
//   - oversize text (> MaxTextBytes) must error
//   - invalid UTF-8 in text must error
//   - oversize vector (len > MaxVectorDim) must error
//   - NaN or ±Inf anywhere in the vector must error
//   - the reverse: an accepted item satisfies every bound above
func FuzzValidatePublishItem(f *testing.F) {
	f.Add("hello", []byte{})
	f.Add("", []byte{0x00, 0x00, 0x00, 0x00})
	f.Add(strings.Repeat("a", MaxTextBytes+1), []byte{})
	f.Add("", []byte{})
	f.Add("text", []byte{0x3f, 0x80, 0x00, 0x00}) // both: text + 1 float
	// NaN bit pattern (quiet NaN) as a single float32.
	f.Add("", []byte{0x00, 0x00, 0xc0, 0x7f})
	// +Inf and -Inf as two consecutive float32s.
	f.Add("", []byte{0x00, 0x00, 0x80, 0x7f, 0x00, 0x00, 0x80, 0xff})
	// Invalid UTF-8 in text.
	f.Add("\xff\xfe", []byte{})
	// Oversize vector: pack just over MaxVectorDim float32s of zero.
	f.Add("", make([]byte, (MaxVectorDim+1)*4))

	f.Fuzz(func(t *testing.T, text string, vecBytes []byte) {
		// Reinterpret vecBytes as float32 sequence. Uses
		// math.Float32frombits so that crafted bit patterns
		// (including NaN / ±Inf) reach the validator as intended.
		vec := make([]float32, len(vecBytes)/4)
		for i := range vec {
			b := vecBytes[i*4:]
			bits := uint32(b[0]) | uint32(b[1])<<8 | uint32(b[2])<<16 | uint32(b[3])<<24
			vec[i] = math.Float32frombits(bits)
		}
		err := validatePublishItem(PublishItem{Text: text, Vector: vec})

		hasText := text != ""
		hasVec := len(vec) > 0
		anyNaNInf := false
		for _, x := range vec {
			f64 := float64(x)
			if math.IsNaN(f64) || math.IsInf(f64, 0) {
				anyNaNInf = true
				break
			}
		}
		mustFail := (!hasText && !hasVec) ||
			(hasText && len(text) > MaxTextBytes) ||
			(hasText && !utf8.ValidString(text)) ||
			(hasVec && len(vec) > MaxVectorDim) ||
			(hasVec && anyNaNInf)
		if mustFail && err == nil {
			t.Fatalf("expected rejection: text=%q (len=%d, utf8=%v) vecLen=%d nan/inf=%v",
				text, len(text), utf8.ValidString(text), len(vec), anyNaNInf)
		}

		// Reverse: an accepted item satisfies every bound.
		if err == nil {
			if !hasText && !hasVec {
				t.Fatalf("accepted item with neither text nor vector")
			}
			if hasText && len(text) > MaxTextBytes {
				t.Fatalf("accepted oversize text len=%d", len(text))
			}
			if hasText && !utf8.ValidString(text) {
				t.Fatalf("accepted invalid-UTF-8 text %q", text)
			}
			if hasVec && len(vec) > MaxVectorDim {
				t.Fatalf("accepted oversize vector len=%d", len(vec))
			}
			if hasVec && anyNaNInf {
				t.Fatalf("accepted vector with NaN/Inf")
			}
		}
	})
}
