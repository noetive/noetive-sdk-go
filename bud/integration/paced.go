//go:build integration

package integration

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"
)

// paced sends a request once more after the wait the server named, when the
// first answer was a rate_limited refusal.
//
// The suite runs faster than the per-second request limit allows, so a refusal
// is a property of the suite's pace, not of the client. The client itself never
// retries a refusal and must not start: this lives in the test harness only.
// A refused request did nothing, so sending it again is safe for any
// operation. Only short waits are taken; a longer one fails the test as before.
type paced struct{ next *http.Client }

// maxPace is the longest wait paced takes for the server.
const maxPace = 3 * time.Second

func (p paced) Do(req *http.Request) (*http.Response, error) {
	resp, err := p.next.Do(req)
	if err != nil || req.GetBody == nil || strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		return resp, err
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	_ = resp.Body.Close()
	if err != nil {
		return nil, err
	}
	resp.Body = io.NopCloser(bytes.NewReader(body))

	var answer struct {
		Error struct {
			Code         string `json:"code"`
			RetryAfterMs int64  `json:"retry_after_ms"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &answer) != nil || answer.Error.Code != "rate_limited" {
		return resp, nil
	}
	wait := time.Duration(max(answer.Error.RetryAfterMs, 1)) * time.Millisecond
	if wait > maxPace {
		return resp, nil
	}
	select {
	case <-req.Context().Done():
		return resp, nil
	case <-time.After(wait):
	}
	again, err := req.GetBody()
	if err != nil {
		return resp, nil
	}
	retry := req.Clone(req.Context())
	retry.Body = again
	return p.next.Do(retry)
}
