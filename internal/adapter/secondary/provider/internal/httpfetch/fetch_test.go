package httpfetch

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTransportErrorCarriesNoURL(t *testing.T) {
	is, must := assert.New(t), require.New(t)
	// Nothing listens on the loopback port, so client.Do fails at transport
	// level; the request URL embeds the secrets the error must not repeat.
	var out struct{}
	err := Do(context.Background(), NewClient(time.Second),
		"http://127.0.0.1:1/v1/reverse?key=SUPERSECRET&lat=51.4774&lon=-0.061&q=home", nil, &out, nil)
	must.Error(err)
	must.ErrorContains(err, "upstream call failed")
	is.NotContains(err.Error(), "SUPERSECRET")
	is.NotContains(err.Error(), "51.4774")
	is.NotContains(err.Error(), "?", "the query string must never appear in an error")
	is.NotContains(err.Error(), "Get ")
}

func TestBuildErrorCarriesNoURL(t *testing.T) {
	is, must := assert.New(t), require.New(t)
	var out struct{}
	err := Do(context.Background(), NewClient(time.Second), "http://%zz/?key=SUPERSECRET", nil, &out, nil)
	must.Error(err)
	is.NotContains(err.Error(), "SUPERSECRET")
	is.NotContains(err.Error(), "%zz")
}

func TestNon200CarriesOnlyStatus(t *testing.T) {
	is, must := assert.New(t), require.New(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()

	var out struct{}
	err := Do(context.Background(), NewClient(time.Second), srv.URL+"/?lat=51.4774", nil, &out, nil)
	must.Error(err)
	is.Equal("upstream status 429", err.Error())
}

func TestTerminalResponseNotRetried(t *testing.T) {
	is, must := assert.New(t), require.New(t)
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":"limit_exceeded"}`))
	}))
	defer srv.Close()

	check := func(statusCode int, body []byte) (string, bool) {
		if statusCode == http.StatusTooManyRequests && string(body) == `{"error":"limit_exceeded"}` {
			return "limit_exceeded", true
		}
		return "", false
	}

	var out struct{}
	err := Do(context.Background(), NewClient(10*time.Second), srv.URL, nil, &out, check)
	must.Error(err)
	is.Equal("upstream status 429: limit_exceeded (terminal provider failure)", err.Error(), "the error carries the provider's code")
	is.Equal(int32(1), calls.Load(), "a terminal response is not retried")
}

// TestTerminalCodeSanitised pins the code-only rule at the httpfetch seam: a
// check cannot put arbitrary text into an error that reaches logs.
func TestTerminalCodeSanitised(t *testing.T) {
	tests := []struct {
		name string
		code string
	}{
		{name: "empty", code: ""},
		{name: "uppercase", code: "LIMIT_EXCEEDED"},
		{name: "coordinates and key", code: "lat=51.4774&key=SUPERSECRET"},
		{name: "too long", code: strings.Repeat("a", 41)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			is, must := assert.New(t), require.New(t)
			var calls atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				calls.Add(1)
				w.WriteHeader(http.StatusTooManyRequests)
				_, _ = w.Write([]byte(`{"error":"x"}`))
			}))
			defer srv.Close()

			check := func(int, []byte) (string, bool) { return tt.code, true }
			var out struct{}
			err := Do(context.Background(), NewClient(10*time.Second), srv.URL, nil, &out, check)
			must.Error(err)
			is.Equal("upstream status 429 (terminal provider failure)", err.Error(), "the unsafe code is dropped, the terminal outcome kept")
			is.Equal(int32(1), calls.Load())
		})
	}
}

func TestTerminalResponseSkipsRetryAfterWait(t *testing.T) {
	is, must := assert.New(t), require.New(t)
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.Header().Set("Retry-After", "600")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":"limit_exceeded"}`))
	}))
	defer srv.Close()

	check := func(statusCode int, body []byte) (string, bool) {
		if statusCode == http.StatusTooManyRequests && string(body) == `{"error":"limit_exceeded"}` {
			return "limit_exceeded", true
		}
		return "", false
	}

	var out struct{}
	start := time.Now()
	err := Do(context.Background(), NewClient(10*time.Second), srv.URL, nil, &out, check)
	must.Error(err)
	is.Equal(int32(1), calls.Load())
	is.Less(time.Since(start), time.Second, "a terminal response ignores Retry-After and returns at once")
}

// TestTerminalCheckBodyIsCapped pins the prefix size against the constant, so a
// boundary test elsewhere cannot silently stop testing it.
func TestTerminalCheckBodyIsCapped(t *testing.T) {
	is, must := assert.New(t), require.New(t)
	is.Equal(4096, MaxErrorBodyBytes, "the docs name 4KiB; changing the cap is a deliberate act")
	var gotLen atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write(bytes.Repeat([]byte("x"), MaxErrorBodyBytes*2))
	}))
	defer srv.Close()

	check := func(_ int, body []byte) (string, bool) {
		gotLen.Store(int32(len(body)))
		return "", false
	}
	var out struct{}
	must.Error(Do(context.Background(), NewClient(10*time.Second), srv.URL, nil, &out, check))
	is.Equal(int32(MaxErrorBodyBytes), gotLen.Load())
}

func TestNonTerminalCheckKeepsRetrying(t *testing.T) {
	is, must := assert.New(t), require.New(t)
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":"rate_limited"}`))
	}))
	defer srv.Close()

	check := func(int, []byte) (string, bool) { return "", false }

	var out struct{}
	err := Do(context.Background(), NewClient(10*time.Second), srv.URL, nil, &out, check)
	must.Error(err)
	is.Equal(int32(maxRetries+1), calls.Load(), "a check that reports nothing terminal keeps the retry path")
}

func TestCrossHostRedirectRefused(t *testing.T) {
	is, must := assert.New(t), require.New(t)
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		is.Empty(r.Header.Get("X-Api-Key"), "the key must never reach another host")
		w.WriteHeader(http.StatusOK)
	}))
	defer target.Close()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusFound)
	}))
	defer srv.Close()

	var out struct{}
	err := Do(context.Background(), NewClient(time.Second), srv.URL, map[string]string{"X-Api-Key": "secret"}, &out, nil)
	must.Error(err)
	must.ErrorContains(err, "cross-host redirect")
	is.NotContains(err.Error(), target.URL)
}

func TestSameHostRedirectFollowed(t *testing.T) {
	must := require.New(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/old" {
			http.Redirect(w, r, "/new", http.StatusFound)
			return
		}
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	var out struct{ Ok bool }
	must.NoError(Do(context.Background(), NewClient(time.Second), srv.URL+"/old", nil, &out, nil))
}

func TestRetriesTransientFailures(t *testing.T) {
	is, must := assert.New(t), require.New(t)
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) < 3 {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	var out struct{ Ok bool }
	must.NoError(Do(context.Background(), NewClient(10*time.Second), srv.URL, nil, &out, nil))
	is.Equal(int32(3), calls.Load(), "two transient 5xx responses are retried")
	is.True(out.Ok)
}

func TestRetriesExhausted(t *testing.T) {
	is, must := assert.New(t), require.New(t)
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	var out struct{}
	err := Do(context.Background(), NewClient(10*time.Second), srv.URL, nil, &out, nil)
	must.Error(err)
	must.ErrorContains(err, "upstream status 503")
	is.Equal(int32(maxRetries+1), calls.Load(), "bounded attempts, then the error surfaces")
}

func TestClientErrorsNotRetried(t *testing.T) {
	is, must := assert.New(t), require.New(t)
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	var out struct{}
	err := Do(context.Background(), NewClient(10*time.Second), srv.URL, nil, &out, nil)
	must.Error(err)
	is.Equal(int32(1), calls.Load(), "a 404 is not transient")
}

func TestRetryAfterHonoured(t *testing.T) {
	is, must := assert.New(t), require.New(t)
	var calls atomic.Int32
	start := time.Now()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) == 1 {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	var out struct{}
	must.NoError(Do(context.Background(), NewClient(10*time.Second), srv.URL, nil, &out, nil))
	is.Equal(int32(2), calls.Load())
	is.GreaterOrEqual(time.Since(start), time.Second, "the retry waits at least the provider's Retry-After")
}

func TestRetryAfterHeader(t *testing.T) {
	tests := []struct {
		name   string
		header string
		want   time.Duration
	}{
		{name: "absent", header: "", want: 0},
		{name: "seconds under the cap", header: "2", want: 2 * time.Second},
		{name: "above the cap is capped", header: "600", want: 5 * time.Second},
		{name: "non-numeric", header: "soon", want: 0},
		{name: "negative", header: "-1", want: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, retryAfterHeader(tt.header))
		})
	}
}

func TestContextCancelledDuringBackoff(t *testing.T) {
	must := require.New(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	var out struct{}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	err := Do(ctx, NewClient(10*time.Second), srv.URL, nil, &out, nil)
	must.ErrorIs(err, context.DeadlineExceeded)
}

func TestRedirectCapRestored(t *testing.T) {
	must := require.New(t)
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		http.Redirect(w, r, "/loop", http.StatusFound)
	}))
	defer srv.Close()

	var out struct{}
	err := Do(context.Background(), NewClient(10*time.Second), srv.URL+"/loop", nil, &out, nil)
	must.Error(err)
	must.ErrorContains(err, "stopped after 10 redirects")
	must.Less(calls.Load(), int32(15), "an infinite redirect loop must be cut short")
}

func TestAcceptHeaderSent(t *testing.T) {
	is, must := assert.New(t), require.New(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		is.Equal("application/json", r.Header.Get("Accept"))
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	var out struct{}
	must.NoError(Do(context.Background(), NewClient(time.Second), srv.URL, nil, &out, nil))
}

func TestTrailingDataRejected(t *testing.T) {
	must := require.New(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"features":[]}<html>oops</html>`))
	}))
	defer srv.Close()

	var out struct {
		Features []any `json:"features"`
	}
	err := Do(context.Background(), NewClient(time.Second), srv.URL, nil, &out, nil)
	must.Error(err)
	must.ErrorContains(err, "trailing data")
}

func TestContextDeadlinePropagates(t *testing.T) {
	must := require.New(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(200 * time.Millisecond)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	var out struct{}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	err := Do(ctx, NewClient(time.Second), srv.URL, nil, &out, nil)
	must.Error(err)
	must.ErrorIs(err, context.DeadlineExceeded)
}

func TestDecodeErrorCarriesNoBody(t *testing.T) {
	is, must := assert.New(t), require.New(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`<html>SECRET ERROR PAGE WITH 51.4774</html>`))
	}))
	defer srv.Close()

	var out struct{}
	err := Do(context.Background(), NewClient(time.Second), srv.URL, nil, &out, nil)
	must.Error(err)
	must.ErrorContains(err, "decoding upstream response")
	is.NotContains(err.Error(), "SECRET")
	is.NotContains(err.Error(), "51.4774")
	is.NotContains(err.Error(), "<html>")
}
