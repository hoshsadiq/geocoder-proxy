// Package httpfetch is the shared HTTP plumbing for provider adapters:
// a GET with context, status-to-error mapping, and JSON decoding, with
// errors that never carry the request URL (URLs contain coordinates, query
// text and API keys, and error text can reach logs).
package httpfetch

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/hoshsadiq/geocoder-proxy/internal/core/domain"
)

// maxBodyBytes caps one upstream response. Geocoding answers are kilobytes;
// the cap exists so a misbehaving upstream cannot exhaust memory.
const maxBodyBytes = 16 << 20

// MaxErrorBodyBytes caps the body prefix offered to a TerminalCheck. A
// provider's error code sits in a small JSON document, and a large error page
// must not be buffered or quoted.
const MaxErrorBodyBytes = 4 << 10

// Redirect policy failures are deterministic: looping or cross-host
// redirects must not be retried.
var (
	errTooManyRedirects  = errors.New("stopped after 10 redirects")
	errCrossHostRedirect = errors.New("refusing cross-host redirect")
)

// NewClient returns the HTTP client provider adapters should use: a timeout
// (upstream slowness must not outlive the caller by minutes) and a redirect
// policy that only follows same-host redirects, because custom auth headers
// (X-Api-Key) are re-sent on cross-host redirects.
func NewClient(timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout: timeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return errTooManyRedirects
			}
			if len(via) > 0 && req.URL.Host != via[0].URL.Host {
				return errCrossHostRedirect
			}
			return nil
		},
	}
}

// maxRetries bounds the attempts per call. Each retry is one more upstream
// request for one router-paced slot, so retries stay few and only for
// clearly transient failures; persistent failure surfaces to the router,
// which fails over to the next provider.
const maxRetries = 2

// TerminalCheck reports whether a non-200 response is a terminal provider
// failure (domain.ErrProviderTerminal), one that will not clear by retrying.
// Such a response ends the call at once so the router fails over, instead of
// spending the provider's remaining allowance on retries that cannot succeed.
// body is a prefix of the response, at most MaxErrorBodyBytes long. The check
// gets a private copy of that prefix, so keeping it is harmless, but the
// response stream is not available again. Provider packages own their upstream's
// vocabulary and pass their check to Do; a nil check retries every transient
// status.
type TerminalCheck func(statusCode int, body []byte) bool

// Do issues a GET and JSON-decodes the response into out, retrying clearly
// transient failures (transport errors, 5xx, 429) with bounded backoff. A
// non-200 response the check reports as terminal is returned at once, without a
// retry. A non-200 status is an error carrying only the status code. Response
// bodies are drained and closed on every path.
func Do(ctx context.Context, client *http.Client, rawURL string, headers map[string]string, out any, terminal TerminalCheck) error {
	return do(ctx, client, rawURL, headers, out, terminal, 0)
}

func do(ctx context.Context, client *http.Client, rawURL string, headers map[string]string, out any, terminal TerminalCheck, attempt int) error {
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
		if !retryableTransport(ctx, err) || attempt >= maxRetries {
			return fmt.Errorf("upstream call failed: %w", err)
		}
		if !sleep(ctx, backoff(attempt, 0)) {
			return fmt.Errorf("upstream call failed: %w", ctx.Err())
		}
		return do(ctx, client, rawURL, headers, out, terminal, attempt+1)
	}
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxBodyBytes))
		_ = resp.Body.Close()
	}()

	if resp.StatusCode != http.StatusOK {
		if classifyTerminal(terminal, resp) {
			return fmt.Errorf("upstream status %d (%w)", resp.StatusCode, domain.ErrProviderTerminal)
		}
		if retryableStatus(resp.StatusCode) && attempt < maxRetries {
			if !sleep(ctx, backoff(attempt, retryAfterHeader(resp.Header.Get("Retry-After")))) {
				return fmt.Errorf("upstream call failed: %w", ctx.Err())
			}
			return do(ctx, client, rawURL, headers, out, terminal, attempt+1)
		}
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

// retryableTransport reports whether a transport-level failure is worth
// retrying: everything except a caller-side cancellation (the caller is
// gone, no answer is wanted) or a TLS verification error (deterministic).
func retryableTransport(ctx context.Context, err error) bool {
	if ctx.Err() != nil {
		return false
	}
	if _, ok := errors.AsType[*tls.CertificateVerificationError](err); ok {
		return false
	}
	return !errors.Is(err, errTooManyRedirects) && !errors.Is(err, errCrossHostRedirect)
}

// classifyTerminal asks the provider's check about a non-200 response, reading
// at most MaxErrorBodyBytes so a huge error page is never buffered. A nil check
// or a read failure is "not terminal", which keeps the retry path.
func classifyTerminal(check TerminalCheck, resp *http.Response) bool {
	if check == nil {
		return false
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, MaxErrorBodyBytes))
	if err != nil {
		return false
	}
	return check(resp.StatusCode, body)
}

// retryableStatus limits retries to server errors (a blip) and 429 (the
// provider's own "come back later"), never to client errors.
func retryableStatus(code int) bool {
	return code == http.StatusTooManyRequests || code >= 500
}

// retryAfterHeader parses a Retry-After header, capped: a provider asking
// for a long wait gets failover instead of a parked caller.
func retryAfterHeader(v string) time.Duration {
	const cap = 5 * time.Second
	if v == "" {
		return 0
	}
	secs, err := strconv.Atoi(v)
	if err != nil || secs < 0 {
		return 0
	}
	if d := time.Duration(secs) * time.Second; d < cap {
		return d
	}
	return cap
}

// backoff waits base*2^attempt with jitter, unless the provider named a
// longer wait via Retry-After.
func backoff(attempt int, retryAfter time.Duration) time.Duration {
	base := 500 * time.Millisecond << attempt
	jitter := time.Duration(rand.Int63n(int64(base / 4)))
	d := base + jitter
	if retryAfter > d {
		return retryAfter
	}
	return d
}

// sleep waits d, returning false if the context ended first.
func sleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
