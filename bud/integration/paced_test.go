//go:build integration

package integration

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// TestPacedResendsARateLimitedRequestOnce covers the harness's one retry: a
// rate_limited refusal is sent again, body and all, once; any other refusal,
// and a second rate_limited, come back as they are. Without it the live suite
// fails on its own pace; with a wider retry it would hide a real refusal.
func TestPacedResendsARateLimitedRequestOnce(t *testing.T) {
	for _, tc := range []struct {
		name     string
		answers  []string
		attempts int64
		want     string
	}{
		{"rate limited, then answered", []string{`{"error":{"code":"rate_limited","retry_after_ms":5}}`, `{"ok":true}`}, 2, `{"ok":true}`},
		{"rate limited twice", []string{`{"error":{"code":"rate_limited","retry_after_ms":5}}`, `{"error":{"code":"rate_limited","retry_after_ms":5}}`}, 2, "rate_limited"},
		{"a quota is not paced", []string{`{"error":{"code":"quota_exceeded","retry_after_ms":5}}`}, 1, "quota_exceeded"},
		{"a long wait is not taken", []string{`{"error":{"code":"rate_limited","retry_after_ms":60000}}`}, 1, "rate_limited"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var n atomic.Int64
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				i := n.Add(1) - 1
				if b, _ := io.ReadAll(r.Body); string(b) != `{"q":"x"}` {
					t.Errorf("attempt %d carried body %q", i+1, b)
				}
				w.WriteHeader(http.StatusTooManyRequests)
				_, _ = io.WriteString(w, tc.answers[min(int(i), len(tc.answers)-1)])
			}))
			defer srv.Close()

			req, _ := http.NewRequestWithContext(t.Context(), http.MethodPost, srv.URL, strings.NewReader(`{"q":"x"}`))
			resp, err := paced{next: srv.Client()}.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			got, _ := io.ReadAll(resp.Body)
			if n.Load() != tc.attempts || !strings.Contains(string(got), tc.want) {
				t.Errorf("%d attempts, answer %s; want %d and %s", n.Load(), got, tc.attempts, tc.want)
			}
		})
	}
}
