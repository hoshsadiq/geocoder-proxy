// Package httpfetch is the shared HTTP plumbing for provider adapters:
// a GET with context, status-to-error mapping, and JSON decoding, with
// errors that never carry the request URL (URLs contain coordinates, query
// text and API keys, and error text can reach logs).
package httpfetch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

// maxBodyBytes caps one upstream response. Geocoding answers are kilobytes;
// the cap exists so a misbehaving upstream cannot exhaust memory.
const maxBodyBytes = 16 << 20

// NewClient returns the HTTP client provider adapters should use: a timeout
// (upstream slowness must not outlive the caller by minutes) and a redirect
// policy that only follows same-host redirects, because custom auth headers
// (X-Api-Key) are re-sent on cross-host redirects.
func NewClient(timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout: timeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) > 0 && req.URL.Host != via[0].URL.Host {
				return fmt.Errorf("refusing cross-host redirect")
			}
			return nil
		},
	}
}

// Do issues a GET and JSON-decodes the response into out. A non-200 status
// is an error carrying only the status code. Response bodies are drained
// and closed on every path.
func Do(ctx context.Context, client *http.Client, rawURL string, headers map[string]string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return errors.New("building request failed")
	}
	req.Header.Set("Accept", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	resp, err := client.Do(req)
	if err != nil {
		// *url.Error formats as `Get "<full url>": cause`; the URL carries
		// coordinates, query text and API keys, so only the cause survives.
		if uerr, ok := errors.AsType[*url.Error](err); ok {
			err = uerr.Err
		}
		return fmt.Errorf("upstream call failed: %w", err)
	}
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxBodyBytes))
		_ = resp.Body.Close()
	}()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("upstream status %d", resp.StatusCode)
	}

	dec := json.NewDecoder(io.LimitReader(resp.Body, maxBodyBytes))
	if err := dec.Decode(out); err != nil {
		return fmt.Errorf("decoding upstream response: %w", err)
	}
	// A well-formed response is exactly one JSON document; trailing bytes
	// mean a mangled or stitched body and must not pass as an answer.
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("decoding upstream response: trailing data after JSON document")
	}
	return nil
}
