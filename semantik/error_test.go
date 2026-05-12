package semantik

import (
	"errors"
	"net/http"
	"testing"
	"time"
)

func TestError_IsMatchesByCode(t *testing.T) {
	cases := []struct {
		err    *Error
		target error
		want   bool
	}{
		{&Error{Code: CodeBackpressure, HTTPStatus: 429}, ErrBackpressure, true},
		{&Error{Code: CodeBackpressure}, ErrRateLimited, false},
		{&Error{Code: CodeUnauthorized}, ErrUnauthorized, true},
		{&Error{Code: CodeInvalidRequest, HTTPStatus: 0}, ErrInvalidRequest, true},
		{nil, ErrBackpressure, false},
	}
	for _, tc := range cases {
		got := errors.Is(tc.err, tc.target)
		if got != tc.want {
			t.Errorf("errors.Is(%v, %v) = %v, want %v", tc.err, tc.target, got, tc.want)
		}
	}
}

func TestError_AsExtractsFields(t *testing.T) {
	orig := &Error{
		Code:       CodeBackpressure,
		Message:    "slow down",
		HTTPStatus: 429,
		RetryAfter: 250 * time.Millisecond,
	}
	var got *Error
	if !errors.As(error(orig), &got) {
		t.Fatal("errors.As failed to extract *Error")
	}
	if got.Code != CodeBackpressure || got.RetryAfter != 250*time.Millisecond {
		t.Errorf("extracted Error has wrong fields: %+v", got)
	}
}

func TestDecodeError_ParsesRetryAfter(t *testing.T) {
	resp := &http.Response{
		StatusCode: http.StatusTooManyRequests,
		Body:       stringBody(`{"error":"backpressure","message":"queue full","retry_after_ms":150}`),
	}
	defer resp.Body.Close()
	e := decodeError(resp)
	if e.Code != CodeBackpressure {
		t.Errorf("Code = %q, want %q", e.Code, CodeBackpressure)
	}
	if e.Message != "queue full" {
		t.Errorf("Message = %q", e.Message)
	}
	if e.RetryAfter != 150*time.Millisecond {
		t.Errorf("RetryAfter = %v, want 150ms", e.RetryAfter)
	}
	if e.HTTPStatus != 429 {
		t.Errorf("HTTPStatus = %d, want 429", e.HTTPStatus)
	}
}

// TestDecodeError_PopulatesRequestIDFromBody locks in the SDK's contract
// for the request_id correlation field added in API version 0.4.0:
//   - the JSON body's request_id is the canonical source
//   - the header is the fallback when the body is empty or malformed
//   - the body wins when both are present (server is authoritative)
func TestDecodeError_PopulatesRequestIDFromBody(t *testing.T) {
	h := http.Header{}
	h.Set("X-Request-Id", "header-fallback")
	resp := &http.Response{
		StatusCode: http.StatusInternalServerError,
		Body:       stringBody(`{"error":"internal_error","request_id":"req_body_value"}`),
		Header:     h,
	}
	defer resp.Body.Close()
	e := decodeError(resp)
	if e.RequestID != "req_body_value" {
		t.Errorf("RequestID = %q, want body value %q", e.RequestID, "req_body_value")
	}
}

// TestDecodeError_FallsBackToRequestIDHeader verifies the X-Request-Id
// response header is used when the body is empty (e.g. middleware-rejected
// 429 with a body the application never gets to write).
func TestDecodeError_FallsBackToRequestIDHeader(t *testing.T) {
	h := http.Header{}
	h.Set("X-Request-Id", "req_header_only")
	resp := &http.Response{
		StatusCode: http.StatusServiceUnavailable,
		Body:       stringBody(""),
		Header:     h,
	}
	defer resp.Body.Close()
	e := decodeError(resp)
	if e.RequestID != "req_header_only" {
		t.Errorf("RequestID = %q, want header fallback %q", e.RequestID, "req_header_only")
	}
}

// TestDecodeError_RecognisesSentinels ensures the per-code sentinels
// round-trip through decodeError so callers can branch on errors.Is.
func TestDecodeError_RecognisesSentinels(t *testing.T) {
	cases := []struct {
		body string
		want error
	}{
		{`{"error":"namespace_unavailable"}`, ErrNamespaceUnavailable},
		{`{"error":"model_not_provisioned"}`, ErrModelNotProvisioned},
		{`{"error":"namespace_disabled"}`, ErrNamespaceDisabled},
		{`{"error":"metering_unavailable"}`, ErrMeteringUnavailable},
	}
	for _, tc := range cases {
		resp := &http.Response{
			StatusCode: http.StatusServiceUnavailable,
			Body:       stringBody(tc.body),
			Header:     http.Header{},
		}
		e := decodeError(resp)
		_ = resp.Body.Close()
		if !errors.Is(e, tc.want) {
			t.Errorf("body %s did not match sentinel: code=%q", tc.body, e.Code)
		}
	}
}

func TestDecodeError_FallsBackOnEmptyBody(t *testing.T) {
	resp := &http.Response{
		StatusCode: http.StatusUnauthorized,
		Body:       stringBody(""),
	}
	defer resp.Body.Close()
	e := decodeError(resp)
	if e.Code != CodeUnauthorized {
		t.Errorf("Code = %q, want %q", e.Code, CodeUnauthorized)
	}
	if e.HTTPStatus != 401 {
		t.Errorf("HTTPStatus = %d, want 401", e.HTTPStatus)
	}
}

func TestDecodeError_Unavailable_BodyField(t *testing.T) {
	resp := &http.Response{
		StatusCode: http.StatusServiceUnavailable,
		Body:       stringBody(`{"error":"unavailable","retry_after_ms":100}`),
		Header:     http.Header{},
	}
	defer resp.Body.Close()
	e := decodeError(resp)
	if !errors.Is(e, ErrUnavailable) {
		t.Errorf("want ErrUnavailable match, got code=%q", e.Code)
	}
	if e.RetryAfter != 100*time.Millisecond {
		t.Errorf("RetryAfter = %v, want 100ms", e.RetryAfter)
	}
}

func TestDecodeError_RetryAfterHeader_Fallback(t *testing.T) {
	// No body — header is the only source of RetryAfter.
	h := http.Header{}
	h.Set("Retry-After", "2")
	resp := &http.Response{
		StatusCode: http.StatusServiceUnavailable,
		Body:       stringBody(""),
		Header:     h,
	}
	defer resp.Body.Close()
	e := decodeError(resp)
	if e.Code != CodeUnavailable {
		t.Errorf("Code = %q, want %q", e.Code, CodeUnavailable)
	}
	if e.RetryAfter != 2*time.Second {
		t.Errorf("RetryAfter = %v, want 2s from header", e.RetryAfter)
	}
}

func TestDecodeError_BodyOverridesHeader(t *testing.T) {
	h := http.Header{}
	h.Set("Retry-After", "5")
	resp := &http.Response{
		StatusCode: http.StatusServiceUnavailable,
		Body:       stringBody(`{"error":"unavailable","retry_after_ms":150}`),
		Header:     h,
	}
	defer resp.Body.Close()
	e := decodeError(resp)
	if e.RetryAfter != 150*time.Millisecond {
		t.Errorf("body retry_after_ms should win; got %v", e.RetryAfter)
	}
}

func TestDecodeError_RetryAfterHeader_HTTPDate_Future(t *testing.T) {
	future := time.Now().Add(5 * time.Second).UTC().Format(http.TimeFormat)
	h := http.Header{}
	h.Set("Retry-After", future)
	resp := &http.Response{
		StatusCode: http.StatusServiceUnavailable,
		Body:       stringBody(""),
		Header:     h,
	}
	defer resp.Body.Close()
	e := decodeError(resp)
	// Allow a wide tolerance — HTTP-date has 1-second resolution and
	// the test may lose time to the round-trip.
	if e.RetryAfter < 3*time.Second || e.RetryAfter > 7*time.Second {
		t.Errorf("RetryAfter = %v, want ~5s from HTTP-date", e.RetryAfter)
	}
}

func TestDecodeError_RetryAfterHeader_HTTPDate_Past(t *testing.T) {
	past := time.Now().Add(-1 * time.Hour).UTC().Format(http.TimeFormat)
	h := http.Header{}
	h.Set("Retry-After", past)
	resp := &http.Response{
		StatusCode: http.StatusServiceUnavailable,
		Body:       stringBody(""),
		Header:     h,
	}
	defer resp.Body.Close()
	e := decodeError(resp)
	if e.RetryAfter != 0 {
		t.Errorf("past HTTP-date should yield 0 retry, got %v", e.RetryAfter)
	}
}

func TestDecodeError_RetryAfterHeader_Unparseable(t *testing.T) {
	h := http.Header{}
	h.Set("Retry-After", "not-a-date-or-number")
	resp := &http.Response{
		StatusCode: http.StatusServiceUnavailable,
		Body:       stringBody(""),
		Header:     h,
	}
	defer resp.Body.Close()
	e := decodeError(resp)
	if e.RetryAfter != 0 {
		t.Errorf("unparseable header should yield 0 retry, got %v", e.RetryAfter)
	}
}

func TestDecodeError_MethodNotAllowedFallback(t *testing.T) {
	resp := &http.Response{
		StatusCode: http.StatusMethodNotAllowed,
		Body:       stringBody(""),
		Header:     http.Header{},
	}
	defer resp.Body.Close()
	e := decodeError(resp)
	if !errors.Is(e, ErrMethodNotAllowed) {
		t.Errorf("want ErrMethodNotAllowed, got %v", e)
	}
}

func TestDecodeError_FallsBackOnMalformedJSON(t *testing.T) {
	resp := &http.Response{
		StatusCode: http.StatusBadRequest,
		Body:       stringBody(`<html>gateway</html>`),
	}
	defer resp.Body.Close()
	e := decodeError(resp)
	if e.Code != CodeInvalidRequest {
		t.Errorf("Code = %q, want %q", e.Code, CodeInvalidRequest)
	}
	if e.Message == "" {
		t.Error("Message should carry fallback body")
	}
}

func TestError_PreflightSentinel(t *testing.T) {
	e := preflightErr("bad input: %d", 42)
	if e.HTTPStatus != 0 {
		t.Errorf("preflight HTTPStatus = %d, want 0", e.HTTPStatus)
	}
	if !errors.Is(e, ErrInvalidRequest) {
		t.Error("preflight error should match ErrInvalidRequest")
	}
	if e.Error() == "" {
		t.Error("Error() must be non-empty")
	}
}

func TestError_StringFormats(t *testing.T) {
	cases := []struct {
		in   *Error
		want string
	}{
		{&Error{Code: CodeInvalidRequest}, "semantik: invalid_request"},
		{&Error{Code: CodeInvalidRequest, Message: "bad"}, "semantik: invalid_request: bad"},
		{&Error{Code: CodeBackpressure, HTTPStatus: 429}, "semantik: 429 backpressure"},
		{&Error{Code: CodeBackpressure, HTTPStatus: 429, Message: "slow"}, "semantik: 429 backpressure: slow"},
	}
	for _, tc := range cases {
		if got := tc.in.Error(); got != tc.want {
			t.Errorf("Error() = %q, want %q", got, tc.want)
		}
	}
}
