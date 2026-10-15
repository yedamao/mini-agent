package llm

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

const okBody = `{"content":[{"type":"text","text":"hi"}],"stop_reason":"end_turn","usage":{"input_tokens":3,"output_tokens":1}}`

// flaky returns a server that answers with the given statuses in order,
// then 200 OK forever, and counts the requests it got.
func flaky(t *testing.T, statuses []int, header http.Header) (*httptest.Server, *atomic.Int32) {
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		i := int(n.Add(1)) - 1
		if i < len(statuses) {
			for k, v := range header {
				w.Header()[k] = v
			}
			w.WriteHeader(statuses[i])
			w.Write([]byte(`{"error":"nope"}`))
			return
		}
		w.Write([]byte(okBody))
	}))
	t.Cleanup(srv.Close)
	return srv, &n
}

// client returns a Client pointed at srv that records waits instead of sleeping.
func client(srv *httptest.Server, waits *[]time.Duration) *Client {
	return &Client{
		BaseURL: srv.URL, Model: "m", APIKey: "k", MaxAttempts: 5,
		sleep: func(ctx context.Context, d time.Duration) error { *waits = append(*waits, d); return ctx.Err() },
	}
}

func TestRetriesTransientErrorsThenSucceeds(t *testing.T) {
	srv, n := flaky(t, []int{500, 529, 429}, nil)
	var waits []time.Duration
	r, err := client(srv, &waits).Call(context.Background(), Request{})
	if err != nil || r.Content[0].Text != "hi" {
		t.Fatalf("got %v, %v", r, err)
	}
	if n.Load() != 4 || len(waits) != 3 {
		t.Errorf("requests=%d waits=%v, want 4 requests and 3 waits", n.Load(), waits)
	}
	// Exponential with equal jitter: attempt k waits in [base*2^(k-1)/2, base*2^(k-1)].
	for k, w := range waits {
		hi := time.Second << k
		if w < hi/2 || w > hi {
			t.Errorf("wait %d = %s, want in [%s, %s]", k+1, w, hi/2, hi)
		}
	}
}

func TestHonorsRetryAfter(t *testing.T) {
	srv, _ := flaky(t, []int{429}, http.Header{"Retry-After": {"7"}})
	var waits []time.Duration
	if _, err := client(srv, &waits).Call(context.Background(), Request{}); err != nil {
		t.Fatal(err)
	}
	if len(waits) != 1 || waits[0] != 7*time.Second {
		t.Errorf("waits = %v, want [7s]", waits)
	}
}

func TestNoRetryOnClientError(t *testing.T) {
	srv, n := flaky(t, []int{400, 400}, nil)
	var waits []time.Duration
	_, err := client(srv, &waits).Call(context.Background(), Request{})
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != 400 {
		t.Fatalf("err = %v, want api 400", err)
	}
	if n.Load() != 1 || len(waits) != 0 {
		t.Errorf("requests=%d waits=%d, want 1 and 0", n.Load(), len(waits))
	}
}

func TestGivesUpAfterMaxAttempts(t *testing.T) {
	srv, n := flaky(t, []int{503, 503, 503, 503, 503, 503}, nil)
	var waits []time.Duration
	_, err := client(srv, &waits).Call(context.Background(), Request{})
	if err == nil || n.Load() != 5 {
		t.Errorf("err=%v requests=%d, want an error after 5 requests", err, n.Load())
	}
}

func TestPerCallTimeoutIsRetried(t *testing.T) {
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if n.Add(1) == 1 {
			select { // first call hangs past the per-call timeout
			case <-time.After(2 * time.Second):
			case <-r.Context().Done():
			}
			return
		}
		w.Write([]byte(okBody))
	}))
	defer srv.Close()
	var waits []time.Duration
	c := client(srv, &waits)
	c.CallTimeout = 100 * time.Millisecond
	if _, err := c.Call(context.Background(), Request{}); err != nil {
		t.Fatalf("err = %v", err)
	}
	if n.Load() != 2 {
		t.Errorf("requests = %d, want 2", n.Load())
	}
}

func TestCancelStopsRetries(t *testing.T) {
	srv, n := flaky(t, []int{503, 503, 503}, nil)
	ctx, cancel := context.WithCancel(context.Background())
	c := &Client{BaseURL: srv.URL, Model: "m", APIKey: "k",
		sleep: func(context.Context, time.Duration) error { cancel(); return context.Canceled }}
	_, err := c.Call(ctx, Request{})
	if !errors.Is(err, context.Canceled) || n.Load() != 1 {
		t.Errorf("err=%v requests=%d, want Canceled after 1 request", err, n.Load())
	}
}

func TestParseRetryAfter(t *testing.T) {
	if d := parseRetryAfter("3"); d != 3*time.Second {
		t.Errorf("seconds: %s", d)
	}
	date := time.Now().Add(10 * time.Second).UTC().Format(http.TimeFormat)
	if d := parseRetryAfter(date); d < 8*time.Second || d > 10*time.Second {
		t.Errorf("date: %s", d)
	}
	if parseRetryAfter("soon") != 0 {
		t.Error("garbage should be 0")
	}
}
