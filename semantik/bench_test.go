package semantik

import (
	"bytes"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	gojson "github.com/goccy/go-json"
)

// --- Encode benchmarks -------------------------------------------------

func BenchmarkSearchEncode(b *testing.B) {
	req := SearchRequest{
		Query:      `MATCH DISTANCE("machine learning research") WITHIN 0.4 LIMIT 10`,
		Namespace:  "articles",
		Model:      "text-embedding-3-small",
		Dimensions: 384,
		Limit:      10,
	}
	b.ReportAllocs()
	for b.Loop() {
		body, release, err := encodeJSON(req)
		if err != nil {
			b.Fatal(err)
		}
		_ = body
		release()
	}
}

func BenchmarkPublishTextEncode(b *testing.B) {
	req := PublishRequest{
		Namespace:  "articles",
		Model:      "text-embedding-3-small",
		Dimensions: 384,
		Items:      []PublishItem{{Text: "Transformer models have reshaped NLP benchmarks."}},
		Metadata:   map[string]string{"source": "arxiv", "author": "jdoe"},
		Ack:        AckStored,
	}
	b.ReportAllocs()
	for b.Loop() {
		body, release, err := encodeJSON(req)
		if err != nil {
			b.Fatal(err)
		}
		_ = body
		release()
	}
}

func BenchmarkPublishVectorEncode384(b *testing.B)  { benchPublishVector(b, 384) }
func BenchmarkPublishVectorEncode1536(b *testing.B) { benchPublishVector(b, 1536) }
func BenchmarkPublishVectorEncode4096(b *testing.B) { benchPublishVector(b, 4096) }

func benchPublishVector(b *testing.B, dim int) {
	vec := make([]float32, dim)
	for i := range vec {
		vec[i] = float32(i%100) * 0.01
	}
	req := PublishRequest{
		Namespace:  "articles",
		Model:      "text-embedding-3-small",
		Dimensions: uint16(dim),
		Items:      []PublishItem{{Vector: vec}},
		Ack:        AckStored,
	}
	b.ReportAllocs()
	for b.Loop() {
		body, release, err := encodeJSON(req)
		if err != nil {
			b.Fatal(err)
		}
		_ = body
		release()
	}
}

func BenchmarkLintEncode(b *testing.B) {
	req := LintRequest{
		Query:  `MATCH DISTANCE("machine learning") WITHIN `,
		Cursor: 38,
	}
	b.ReportAllocs()
	for b.Loop() {
		body, release, err := encodeJSON(req)
		if err != nil {
			b.Fatal(err)
		}
		_ = body
		release()
	}
}

func BenchmarkSubscribeEncode(b *testing.B) {
	req := SubscribeRequest{
		Query:      `MATCH DISTANCE("gpu shortage") WITHIN 0.5`,
		Namespace:  "articles",
		Model:      "text-embedding-3-small",
		Dimensions: 384,
	}
	b.ReportAllocs()
	for b.Loop() {
		body, release, err := encodeJSON(req)
		if err != nil {
			b.Fatal(err)
		}
		_ = body
		release()
	}
}

// --- Response decode benchmarks ---------------------------------------

func BenchmarkSearchResponseDecode1(b *testing.B)  { benchSearchResponseDecode(b, 1) }
func BenchmarkSearchResponseDecode50(b *testing.B) { benchSearchResponseDecode(b, 50) }

func benchSearchResponseDecode(b *testing.B, n int) {
	resp := SearchResponse{Results: make([]ResultItem, n)}
	for i := range resp.Results {
		resp.Results[i] = ResultItem{
			MessageID: "msg_abcdef0123456789",
			Namespace: "articles",
			Score:     0.83,
			Content:   "Transformers reshaped NLP benchmarks.",
			Metadata:  map[string]string{"source": "arxiv"},
		}
	}
	body, err := gojson.Marshal(resp)
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.SetBytes(int64(len(body)))
	for b.Loop() {
		var out SearchResponse
		if err := safeUnmarshal(body, &out); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkPublishResponseDecode(b *testing.B) {
	body := []byte(`{"message_id":"msg_abcdef0123456789","epoch":4223372,"seq":7881209}`)
	b.ReportAllocs()
	b.SetBytes(int64(len(body)))
	for b.Loop() {
		var out PublishResponse
		if err := safeUnmarshal(body, &out); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkLintResponseDecode(b *testing.B) {
	body := []byte(`{"valid":false,"normalized":"","diagnostics":[{"severity":"error","message":"expected keyword","line":1,"col":1,"end_line":1,"end_col":1}],"completions":[{"label":"MATCH","kind":"keyword","detail":"Begin a query"},{"label":"WHERE","kind":"clause","detail":"Filter clause"}]}`)
	b.ReportAllocs()
	b.SetBytes(int64(len(body)))
	for b.Loop() {
		var out LintResponse
		if err := safeUnmarshal(body, &out); err != nil {
			b.Fatal(err)
		}
	}
}

// --- Error-path benchmarks --------------------------------------------

func BenchmarkErrorDecode(b *testing.B) {
	body := `{"error":"backpressure","message":"queue full","retry_after_ms":150}`
	b.ReportAllocs()
	for b.Loop() {
		resp := &http.Response{
			StatusCode: http.StatusTooManyRequests,
			Body:       io.NopCloser(strings.NewReader(body)),
		}
		e := decodeError(resp)
		if e == nil {
			b.Fatal("nil error")
		}
		_ = resp.Body.Close()
	}
}

func BenchmarkErrorDecode_503WithHeader(b *testing.B) {
	body := `{"error":"unavailable"}`
	h := http.Header{}
	h.Set("Retry-After", "2")
	b.ReportAllocs()
	for b.Loop() {
		resp := &http.Response{
			StatusCode: http.StatusServiceUnavailable,
			Body:       io.NopCloser(strings.NewReader(body)),
			Header:     h,
		}
		e := decodeError(resp)
		if e == nil || e.RetryAfter != 2*time.Second {
			b.Fatalf("unexpected: %+v", e)
		}
		_ = resp.Body.Close()
	}
}

// --- Helper benchmarks (hot-path primitives) ---------------------------

func BenchmarkValidateMetadata16(b *testing.B) {
	md := make(map[string]string, MaxMetadataKeys)
	for i := range MaxMetadataKeys {
		md[string(rune('a'+i))+"_key"] = strings.Repeat("v", 16)
	}
	b.ReportAllocs()
	for b.Loop() {
		if err := validateMetadata(md); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkRetryAfterHeader_Seconds(b *testing.B) {
	h := http.Header{}
	h.Set("Retry-After", "30")
	resp := &http.Response{Header: h}
	b.ReportAllocs()
	for b.Loop() {
		if d := retryAfterFromHeader(resp); d != 30*time.Second {
			b.Fatalf("got %v", d)
		}
	}
}

func BenchmarkRetryAfterHeader_Date(b *testing.B) {
	h := http.Header{}
	// Use a far-future date so the benchmark remains deterministic.
	h.Set("Retry-After", "Wed, 21 Oct 2099 07:28:00 GMT")
	resp := &http.Response{Header: h}
	b.ReportAllocs()
	for b.Loop() {
		if d := retryAfterFromHeader(resp); d <= 0 {
			b.Fatalf("expected positive duration, got %v", d)
		}
	}
}

func BenchmarkEncodeJSONReuse(b *testing.B) {
	// Measures per-call overhead when the pooled buffer is warm.
	req := SearchRequest{Query: "q", Namespace: "n", Model: "m", Dimensions: 384}
	// Prime the pool.
	if body, release, err := encodeJSON(req); err == nil {
		_ = body
		release()
	}
	b.ReportAllocs()
	for b.Loop() {
		body, release, err := encodeJSON(req)
		if err != nil {
			b.Fatal(err)
		}
		_ = body
		release()
	}
}

// --- Decoder against bytes.Reader (no http.Response wrapper) ----------

func BenchmarkSafeDecode_SearchResponse(b *testing.B) {
	body := []byte(`{"results":[{"message_id":"m1","score":0.9,"content":"hi","namespace":"n"}]}`)
	b.ReportAllocs()
	b.SetBytes(int64(len(body)))
	for b.Loop() {
		var out SearchResponse
		if err := safeDecode(bytes.NewReader(body), &out); err != nil {
			b.Fatal(err)
		}
	}
}
