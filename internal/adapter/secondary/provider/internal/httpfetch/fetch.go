// Package httpfetch is the shared HTTP plumbing for provider adapters:
// a GET with context, status-to-error mapping, and JSON decoding, with
// errors that never carry the request URL (URLs contain coordinates or
// query text, and error text can reach logs).
package httpfetch

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// Do issues a GET and JSON-decodes the response into out. A non-200 status
// is an error carrying only the status code. Response bodies are drained
// and closed on every path.
func Do(ctx context.Context, client *http.Client, url string, headers map[string]string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("building request: %w", err)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("upstream call failed: %w", err)
	}
	defer func() {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("upstream status %d", resp.StatusCode)
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("decoding upstream response: %w", err)
	}
	return nil
}
