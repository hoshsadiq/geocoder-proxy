package httpfetch

import (
	"context"
	"net/http"
	"net/http/httptest"
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
		"http://127.0.0.1:1/v1/reverse?key=SUPERSECRET&lat=51.4774&lon=-0.061&q=home", nil, &out)
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
	err := Do(context.Background(), NewClient(time.Second), "http://%zz/?key=SUPERSECRET", nil, &out)
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
	err := Do(context.Background(), NewClient(time.Second), srv.URL+"/?lat=51.4774", nil, &out)
	must.Error(err)
	is.Equal("upstream status 429", err.Error())
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
	err := Do(context.Background(), NewClient(time.Second), srv.URL, map[string]string{"X-Api-Key": "secret"}, &out)
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
	must.NoError(Do(context.Background(), NewClient(time.Second), srv.URL+"/old", nil, &out))
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
	err := Do(context.Background(), NewClient(10*time.Second), srv.URL+"/loop", nil, &out)
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
	must.NoError(Do(context.Background(), NewClient(time.Second), srv.URL, nil, &out))
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
	err := Do(context.Background(), NewClient(time.Second), srv.URL, nil, &out)
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
	err := Do(ctx, NewClient(time.Second), srv.URL, nil, &out)
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
	err := Do(context.Background(), NewClient(time.Second), srv.URL, nil, &out)
	must.Error(err)
	must.ErrorContains(err, "decoding upstream response")
	is.NotContains(err.Error(), "SECRET")
	is.NotContains(err.Error(), "51.4774")
	is.NotContains(err.Error(), "<html>")
}
