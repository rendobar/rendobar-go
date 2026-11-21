package rendobar

// Behaviour beyond the shared scenarios, against httptest servers this file controls.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rendobar/rendobar-go/internal/api/core"
)

func testServer(t *testing.T, h http.HandlerFunc) (*Client, *atomic.Int32) {
	t.Helper()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		h(w, r)
	}))
	t.Cleanup(srv.Close)
	return New(WithAPIKey("rb_test"), WithBaseURL(srv.URL)), &hits
}

func jobJSON(status string, timeoutMs int) string {
	extra := ""
	if timeoutMs > 0 {
		extra = fmt.Sprintf(`,"timeoutMs":%d`, timeoutMs)
	}
	if status == "failed" {
		extra += `,"error":{"code":"INPUT_UNREADABLE","message":"bad input"}`
	}
	return fmt.Sprintf(`{"data":{"id":"job_t","status":%q%s}}`, status, extra)
}

func shorten(t *testing.T, def, grace time.Duration) {
	t.Helper()
	d, g := defaultWaitTimeout, waitGrace
	defaultWaitTimeout, waitGrace = def, grace
	t.Cleanup(func() { defaultWaitTimeout, waitGrace = d, g })
}

func TestWaitTimesOutAndReportsActualWait(t *testing.T) {
	c, _ := testServer(t, func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, jobJSON("running", 0)) })
	started := time.Now()
	_, err := c.Jobs.Wait(context.Background(), "job_t", &WaitOptions{Timeout: 300 * time.Millisecond, Interval: 40 * time.Millisecond})
	var te *WaitTimeoutError
	if !errors.As(err, &te) || te.LastStatus != "running" {
		t.Fatalf("got %v", err)
	}
	if te.Waited < 300*time.Millisecond || te.Waited > time.Since(started) {
		t.Fatalf("Waited %v", te.Waited)
	}
}

func TestWaitDefaultBoundsAJobStuckInTheQueue(t *testing.T) {
	shorten(t, 250*time.Millisecond, 0)
	c, _ := testServer(t, func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, jobJSON("waiting", 0)) })
	started := time.Now()
	_, err := c.Jobs.Wait(context.Background(), "job_t", &WaitOptions{Interval: 40 * time.Millisecond})
	var te *WaitTimeoutError
	if !errors.As(err, &te) || te.LastStatus != "waiting" {
		t.Fatalf("got %v", err)
	}
	if d := time.Since(started); d > 2*time.Second {
		t.Fatalf("waited %v on a queued job, want about the default", d)
	}
}

func TestWaitAdoptsTheBudgetFromThePollThatReportsIt(t *testing.T) {
	shorten(t, 400*time.Millisecond, 0)
	var n atomic.Int32
	c, _ := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		// The first poll answers at about 250 ms, running with a 500 ms budget.
		if n.Add(1) == 1 {
			time.Sleep(250 * time.Millisecond)
		}
		fmt.Fprint(w, jobJSON("running", 500))
	})
	started := time.Now()
	_, err := c.Jobs.Wait(context.Background(), "job_t", &WaitOptions{Interval: 20 * time.Millisecond})
	var te *WaitTimeoutError
	if !errors.As(err, &te) {
		t.Fatalf("got %v", err)
	}
	// Adopted at about 250 ms: deadline about 750 ms. Counting the budget from
	// the start of Wait would end at 500 ms, the bare default at 400 ms.
	if d := time.Since(started); d < 700*time.Millisecond || d > 3*time.Second {
		t.Fatalf("timed out after %v, want about 750ms", d)
	}
	if n.Load() < 3 {
		t.Fatalf("%d polls", n.Load())
	}
}

func TestWaitBudgetNeverShortensTheDeadline(t *testing.T) {
	shorten(t, 600*time.Millisecond, 0)
	c, _ := testServer(t, func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, jobJSON("running", 1)) })
	started := time.Now()
	_, err := c.Jobs.Wait(context.Background(), "job_t", &WaitOptions{Interval: 40 * time.Millisecond})
	var te *WaitTimeoutError
	if !errors.As(err, &te) {
		t.Fatalf("got %v", err)
	}
	if d := time.Since(started); d < 550*time.Millisecond {
		t.Fatalf("a 1 ms budget cut the wait to %v", d)
	}
}

func TestWaitStopsOnContextCancel(t *testing.T) {
	c, _ := testServer(t, func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, jobJSON("running", 0)) })
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, err := c.Jobs.Wait(ctx, "job_t", &WaitOptions{Interval: 10 * time.Second})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("got %v", err)
	}
	if d := time.Since(started); d > 2*time.Second {
		t.Fatalf("returned after %v", d)
	}
}

func TestWaitCallsOnProgressEveryPoll(t *testing.T) {
	var n atomic.Int32
	c, _ := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		if n.Add(1) < 3 {
			fmt.Fprint(w, jobJSON("running", 0))
			return
		}
		fmt.Fprint(w, jobJSON("complete", 0))
	})
	var seen []string
	job, err := c.Jobs.Wait(context.Background(), "job_t", &WaitOptions{Interval: 10 * time.Millisecond, OnProgress: func(j *Job) { seen = append(seen, j.Status) }})
	if err != nil || job.Status != "complete" {
		t.Fatalf("%v %v", job, err)
	}
	if strings.Join(seen, ",") != "running,running,complete" {
		t.Fatalf("seen %v", seen)
	}
}

func TestRunReturnsJobFailedError(t *testing.T) {
	c, _ := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			w.WriteHeader(201)
			fmt.Fprint(w, `{"data":{"id":"job_t","status":"waiting"}}`)
			return
		}
		fmt.Fprint(w, jobJSON("failed", 0))
	})
	job, err := c.Jobs.Run(context.Background(), CreateParams{Type: "ffprobe"}, &WaitOptions{Interval: 10 * time.Millisecond})
	var failed *JobFailedError
	if !errors.As(err, &failed) || failed.Job != job || job.Failed.Error.Code != "INPUT_UNREADABLE" {
		t.Fatalf("got %v, %v", job, err)
	}
	if !strings.Contains(err.Error(), "INPUT_UNREADABLE") {
		t.Fatalf("message %q", err)
	}
}

func TestJobFailedErrorNeverPanics(t *testing.T) {
	cases := []*JobFailedError{
		nil,
		{},
		{Job: &Job{Status: "cancelled"}},
		{Job: &Job{Status: "failed", Failed: &JobFailed{ID: "job_t"}}},
	}
	for _, e := range cases {
		if msg := e.Error(); !strings.HasPrefix(msg, "rendobar: job") {
			t.Fatalf("message %q", msg)
		}
	}
}

func TestAllCanBeRangedTwice(t *testing.T) {
	c, _ := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		offset := r.URL.Query().Get("offset")
		if offset == "" {
			offset = "0"
		}
		var o int
		fmt.Sscan(offset, &o)
		var items []string
		for i := o; i < min(o+2, 5); i++ {
			items = append(items, fmt.Sprintf(`{"id":"job_%d","status":"running"}`, i))
		}
		fmt.Fprintf(w, `{"data":[%s],"meta":{"total":5,"page":1,"limit":2}}`, strings.Join(items, ","))
	})
	seq := c.Jobs.All(context.Background(), ListParams{Limit: 2})
	for round := 0; round < 2; round++ {
		n := 0
		for _, err := range seq {
			if err != nil {
				t.Fatal(err)
			}
			n++
		}
		if n != 5 {
			t.Fatalf("round %d yielded %d, want 5", round, n)
		}
	}
}

func TestWithTimeoutInEitherOrderAndCallersClientUntouched(t *testing.T) {
	for _, order := range []string{"timeout first", "client first"} {
		t.Run(order, func(t *testing.T) {
			mine := &http.Client{Timeout: time.Hour}
			opts := []Option{WithTimeout(200 * time.Millisecond), WithHTTPClient(mine)}
			if order == "client first" {
				opts[0], opts[1] = opts[1], opts[0]
			}
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				select { // slower than the timeout, and gone as soon as the client hangs up
				case <-time.After(2 * time.Second):
				case <-r.Context().Done():
				}
				fmt.Fprint(w, jobJSON("running", 0))
			}))
			defer srv.Close()
			c := New(append(opts, WithAPIKey("rb_test"), WithBaseURL(srv.URL), WithMaxRetries(0))...)
			started := time.Now()
			if _, err := c.Jobs.Get(context.Background(), "job_t"); err == nil {
				t.Fatal("no timeout")
			}
			if d := time.Since(started); d > 1500*time.Millisecond {
				t.Fatalf("took %v, WithTimeout did not apply", d)
			}
			if mine.Timeout != time.Hour || mine.Transport != nil {
				t.Fatalf("caller's client changed: %+v", mine)
			}
		})
	}
}

func TestRetries(t *testing.T) {
	for _, tc := range []struct {
		name    string
		retries int
		status  int
		want    int32
	}{
		{"503 retried by default", -1, 503, 3},
		{"429 retried", 1, 429, 2},
		{"0 means one request", 0, 503, 1},
		{"negative clamps to 0", -5, 503, 1},
		{"409 never retried", 2, 409, 1},
		{"408 never retried", 2, 408, 1},
		{"404 never retried", 2, 404, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var hits atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				hits.Add(1)
				w.Header().Set("Retry-After", "0")
				w.WriteHeader(tc.status)
				fmt.Fprint(w, `{"error":{"code":"X","message":"no"}}`)
			}))
			defer srv.Close()
			opts := []Option{WithAPIKey("rb_test"), WithBaseURL(srv.URL)}
			if tc.retries != -1 {
				opts = append(opts, WithMaxRetries(tc.retries))
			}
			_, err := New(opts...).Jobs.Get(context.Background(), "job_t")
			var apiErr *Error
			if !errors.As(err, &apiErr) || apiErr.StatusCode != tc.status {
				t.Fatalf("got %v", err)
			}
			if hits.Load() != tc.want {
				t.Fatalf("%d requests, want %d", hits.Load(), tc.want)
			}
		})
	}
}

func TestConnectionErrorIsRetriedAndSubmitKeepsItsBody(t *testing.T) {
	var hits atomic.Int32
	var bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(b))
		if hits.Add(1) == 1 {
			hj, _ := w.(http.Hijacker)
			conn, _, _ := hj.Hijack()
			conn.Close() // drop the connection: no response at all
			return
		}
		w.WriteHeader(201)
		fmt.Fprint(w, `{"data":{"id":"job_t","status":"waiting"}}`)
	}))
	defer srv.Close()
	c := New(WithAPIKey("rb_test"), WithBaseURL(srv.URL))
	created, err := c.Jobs.Create(context.Background(), CreateParams{Type: "ffprobe", Params: map[string]any{"command": "x"}})
	if err != nil || created.ID != "job_t" {
		t.Fatalf("%v %v", created, err)
	}
	if hits.Load() != 2 || bodies[0] == "" || bodies[0] != bodies[1] {
		t.Fatalf("hits %d bodies %q", hits.Load(), bodies)
	}
}

func TestErrorUnwrapsAndReadsRetryAfter(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "7")
		w.WriteHeader(429)
		fmt.Fprint(w, `{"error":{"code":"RATE_LIMITED","message":"slow"}}`)
	}))
	defer srv.Close()
	c := New(WithAPIKey("rb_test"), WithBaseURL(srv.URL), WithMaxRetries(0))
	_, err := c.Jobs.Get(context.Background(), "job_t")
	var apiErr *Error
	if !errors.As(err, &apiErr) || apiErr.RetryAfter != 7*time.Second {
		t.Fatalf("got %#v", err)
	}
	var coreErr *core.APIError
	if !errors.As(err, &coreErr) || coreErr.StatusCode != 429 || errors.Unwrap(apiErr) == nil {
		t.Fatalf("does not unwrap to the core error: %v", err)
	}
}

func TestRetryAfterParsing(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for v, want := range map[string]time.Duration{
		"1":   time.Second,
		"0.5": 500 * time.Millisecond,
		now.Add(3 * time.Second).Format(http.TimeFormat): 3 * time.Second,
	} {
		if got, ok := retryAfter(v, now); !ok || got != want {
			t.Fatalf("%q: %v %v", v, got, ok)
		}
	}
	for _, v := range []string{"", "-1", "soon", "NaN"} {
		if _, ok := retryAfter(v, now); ok {
			t.Fatalf("%q parsed", v)
		}
	}
}

func TestRouteCollapsesEveryIDSegment(t *testing.T) {
	for in, want := range map[string]string{
		"/jobs/job_abc123":                     "/jobs/:id",
		"/jobs/job_abc123/logs":                "/jobs/:id/logs",
		"/orgs/org_1/assets/asset_2":           "/orgs/:id/assets/:id",
		"/x/job_a/batch_b":                     "/x/:id/:id",
		"/webhooks/wh_x/keys/key_y/upl_z/rb_k": "/webhooks/:id/keys/:id/:id/:id",
		"/things/thing_Ab12Cd34":               "/things/:id",
		"/billing/checkout/pro":                "/billing/checkout/pro",
		"/api_keys":                            "/api_keys",
		"/upload-sessions/upl_x/complete":      "/upload-sessions/:id/complete",
		"/notes/short_ab":                      "/notes/short_ab",
		"/jobs/types/ffmpeg":                   "/jobs/types/ffmpeg",
	} {
		if got := routeOf(in); got != want {
			t.Fatalf("%s: got %s, want %s", in, got, want)
		}
	}
}

func TestDeprecationReportedOncePerRoute(t *testing.T) {
	d := newDeprecations(nil)
	var notices []DeprecationNotice
	d.handler = func(n DeprecationNotice) { notices = append(notices, n) }
	res := &http.Response{Header: http.Header{"Deprecation": {"@1760659200"}}}
	d.note(res, "GET", "/jobs/job_a/batch_b")
	d.note(res, "GET", "/jobs/job_c/batch_d")
	if len(notices) != 1 || notices[0].Path != "/jobs/job_a/batch_b" {
		t.Fatalf("notices %+v", notices)
	}
}

func TestDeprecationOnStderrNamesTheRealPath(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	stderr := os.Stderr
	os.Stderr = w
	newDeprecations(nil).note(&http.Response{Header: http.Header{"Deprecation": {"@1760659200"}}}, "GET", "/jobs/job_abc123/logs")
	os.Stderr = stderr
	w.Close()
	out, _ := io.ReadAll(r)
	if !strings.Contains(string(out), "GET /jobs/job_abc123/logs is deprecated since 2025-10-17") {
		t.Fatalf("stderr %q", out)
	}
}
