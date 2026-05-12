package semantik

import (
	"context"
	"net/http"
)

// Health performs an unauthenticated liveness probe against the
// Semantik endpoint. Returns nil when the server replied 200. Any other
// response produces an *Error; transport errors are returned raw.
func (c *Client) Health(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+pathHealth, nil)
	if err != nil {
		return err
	}
	req.Header.Set(headerContentType, mimeJSON)
	req.Header.Set(headerUserAgent, UserAgent())
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer drainAndClose(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return decodeError(resp)
	}
	return nil
}
