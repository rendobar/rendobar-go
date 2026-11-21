package rendobar

// Behaviour beyond the shared scenarios, against httptest servers this file controls.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"
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
	if !errors.As(err, &te) || te.LastStatus != "running" || te.Job == nil || te.Job.ID() != "job_t" {
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
	job, err := c.Jobs.Wait(context.Background(), "job_t", &WaitOptions{Interval: 10 * time.Millisecond, OnProgress: func(j *Job) { seen = append(seen, string(j.Status())) }})
	if err != nil || job.Status() != StatusComplete {
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
	job, err := c.Jobs.Run(context.Background(), CreateParams{Params: FfprobeParams{Command: "x"}}, &WaitOptions{Interval: 10 * time.Millisecond})
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
		{Job: &Job{status: StatusCancelled}},
		{Job: &Job{status: StatusFailed, Failed: &JobFailed{JobInfo: JobInfo{ID: "job_t"}}}},
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
	created, err := c.Jobs.Create(context.Background(), CreateParams{Params: FfprobeParams{Command: "x"}})
	if err != nil || created.ID != "job_t" {
		t.Fatalf("%v %v", created, err)
	}
	if hits.Load() != 2 || bodies[0] == "" || bodies[0] != bodies[1] {
		t.Fatalf("hits %d bodies %q", hits.Load(), bodies)
	}
}

func TestErrorReadsRetryAfterAndHidesTheCore(t *testing.T) {
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
	// The core's error types are internal, so *Error never hands one out.
	if errors.Unwrap(apiErr) != nil {
		t.Fatalf("unwraps to %T", errors.Unwrap(apiErr))
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

func TestRunKeepsTheJobIDWhenTheContextEnds(t *testing.T) {
	c, _ := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			w.WriteHeader(201)
			fmt.Fprint(w, `{"data":{"id":"job_t","status":"waiting"}}`)
			return
		}
		fmt.Fprint(w, jobJSON("running", 0))
	})
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	job, err := c.Jobs.Run(ctx, CreateParams{Params: FfprobeParams{Command: "x"}}, &WaitOptions{Interval: 40 * time.Millisecond})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("got %v", err)
	}
	var ce *ContextError
	if !errors.As(err, &ce) || ce.JobID != "job_t" || ce.Job == nil || ce.Job.Status() != StatusRunning {
		t.Fatalf("got %#v", err)
	}
	if job != ce.Job || !strings.Contains(err.Error(), "job_t") {
		t.Fatalf("job %v, message %q", job, err)
	}
}

func TestWaitKeepsTheJobIDWhenCancelledBeforeTheFirstPoll(t *testing.T) {
	c, _ := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		select { // answers only after the caller gave up
		case <-time.After(2 * time.Second):
		case <-r.Context().Done():
		}
		fmt.Fprint(w, jobJSON("running", 0))
	})
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(100*time.Millisecond, cancel)
	job, err := c.Jobs.Wait(ctx, "job_t", nil)
	var ce *ContextError
	if !errors.Is(err, context.Canceled) || !errors.As(err, &ce) || ce.JobID != "job_t" || ce.Job != nil || job != nil {
		t.Fatalf("got %v, %#v", job, err)
	}
}

func TestWaitTimeoutErrorShowsSubSecondWaits(t *testing.T) {
	for waited, want := range map[time.Duration]string{
		341 * time.Millisecond:             "after 341ms",
		2412 * time.Millisecond:            "after 2.4s",
		5*time.Minute + 3*time.Millisecond: "after 5m0s",
	} {
		e := &WaitTimeoutError{JobID: "job_t", LastStatus: StatusDispatched, Waited: waited}
		if !strings.Contains(e.Error(), "still dispatched "+want) {
			t.Fatalf("%v: %q", waited, e.Error())
		}
	}
}

func TestRateLimitLongerThanTheDeadlineStaysRateLimited(t *testing.T) {
	c, hits := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "30")
		w.WriteHeader(429)
		fmt.Fprint(w, `{"error":{"code":"RATE_LIMITED","message":"slow down"}}`)
	})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	started := time.Now()
	_, err := c.Jobs.Get(ctx, "job_t")
	if codeOf(err) != CodeRateLimited {
		t.Fatalf("got %v", err)
	}
	var apiErr *Error
	if !errors.As(err, &apiErr) || apiErr.RetryAfter != 30*time.Second {
		t.Fatalf("got %#v", err)
	}
	if d := time.Since(started); d > time.Second || hits.Load() != 1 {
		t.Fatalf("waited %v over %d requests for a retry that could not happen", d, hits.Load())
	}
}

func codeOf(err error) ErrorCode {
	var e *Error
	if errors.As(err, &e) {
		return e.Code
	}
	return ""
}

func statusOf(err error) int {
	var e *Error
	if errors.As(err, &e) {
		return e.StatusCode
	}
	return 0
}

// keyServer records the idempotency key of every submit and answers with h.
func keyServer(t *testing.T, h http.HandlerFunc, opts ...Option) (*Client, chan string) {
	t.Helper()
	sent := make(chan string, 8)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			IdempotencyKey string `json:"idempotencyKey"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		sent <- body.IdempotencyKey
		h(w, r)
	}))
	t.Cleanup(srv.Close)
	return New(append([]Option{WithAPIKey("rb_test"), WithBaseURL(srv.URL)}, opts...)...), sent
}

func TestSubmitErrorCarriesTheKeyWhenTheJobMayExist(t *testing.T) {
	stall := func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }
	for _, tc := range []struct {
		name    string
		handler http.HandlerFunc
		opts    []Option
		ctx     func() (context.Context, context.CancelFunc)
		is      func(error) bool
	}{
		{"ctx cancelled", stall, nil, func() (context.Context, context.CancelFunc) {
			ctx, cancel := context.WithCancel(context.Background())
			time.AfterFunc(100*time.Millisecond, cancel)
			return ctx, cancel
		}, func(err error) bool { return errors.Is(err, context.Canceled) }},
		{"attempt timed out", stall, []Option{WithTimeout(150 * time.Millisecond)}, nil,
			func(err error) bool { var ue *url.Error; return errors.As(err, &ue) && ue.Timeout() }},
		{"503 after the last retry", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(503)
			fmt.Fprint(w, `{"error":{"code":"UNAVAILABLE","message":"down"}}`)
		}, nil, nil, func(err error) bool { var e *Error; return errors.As(err, &e) && e.StatusCode == 503 }},
		{"connection dropped", func(w http.ResponseWriter, r *http.Request) {
			conn, _, _ := w.(http.Hijacker).Hijack()
			conn.Close()
		}, []Option{WithMaxRetries(0)}, nil, func(err error) bool { return err != nil }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, sent := keyServer(t, tc.handler, tc.opts...)
			ctx, cancel := context.Background(), context.CancelFunc(func() {})
			if tc.ctx != nil {
				ctx, cancel = tc.ctx()
			}
			defer cancel()
			created, err := c.Jobs.Create(ctx, CreateParams{Params: FfprobeParams{Command: "x"}})
			var se *SubmitError
			if created != nil || !errors.As(err, &se) || !tc.is(err) {
				t.Fatalf("got %v, %#v", created, err)
			}
			if key := <-sent; se.IdempotencyKey != key || !strings.HasPrefix(key, "sdk_") || !strings.Contains(err.Error(), key) {
				t.Fatalf("error %q, sent key %q", err, key)
			}
		})
	}
}

func TestSubmitRefusalIsNotASubmitError(t *testing.T) {
	c, _ := keyServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(400)
		fmt.Fprint(w, `{"error":{"code":"VALIDATION_ERROR","message":"bad","details":[{"path":["params","command"],"message":"Required"},{"path":"type","message":"Unknown","upgradeTo":"pro"},"junk"]}}`)
	})
	_, err := c.Jobs.Create(context.Background(), CreateParams{Params: FfprobeParams{Command: "x"}})
	var se *SubmitError
	var apiErr *Error
	if errors.As(err, &se) || !errors.As(err, &apiErr) || apiErr.StatusCode != 400 {
		t.Fatalf("got %#v", err)
	}
	want := []Problem{{Path: "params.command", Message: "Required"}, {Path: "type", Message: "Unknown", UpgradeTo: "pro"}}
	if fmt.Sprint(apiErr.Problems) != fmt.Sprint(want) {
		t.Fatalf("problems %+v", apiErr.Problems)
	}
}

func TestProblemsAreParsedDefensively(t *testing.T) {
	for _, details := range []string{`null`, `"text"`, `{"jobId":"job_1"}`, `[1,"x",null,{"path":"a"}]`, `not json`} {
		if p := problems(json.RawMessage(details)); len(p) != 0 {
			t.Fatalf("%s gave %+v", details, p)
		}
	}
	// Any code with a problem list gets problems, not only a fixed few.
	p := problems(json.RawMessage(`[{"path":["inputs",0],"message":"unreachable","upgradeTo":null}]`))
	if len(p) != 1 || p[0].Path != "inputs.0" || p[0].UpgradeTo != "" {
		t.Fatalf("problems %+v", p)
	}
}

func TestAnUnknownErrorCodeFlowsThrough(t *testing.T) {
	c, _ := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(418)
		fmt.Fprint(w, `{"error":{"code":"BRAND_NEW_CODE","message":"new","details":{"why":"later"}}}`)
	})
	_, err := c.Jobs.Get(context.Background(), "job_t")
	var apiErr *Error
	if !errors.As(err, &apiErr) || apiErr.Code != "BRAND_NEW_CODE" || string(apiErr.Details) != `{"why":"later"}` || apiErr.Problems != nil {
		t.Fatalf("got %#v", err)
	}
	c, _ = testServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(502)
		fmt.Fprint(w, `<html>bad gateway</html>`)
	})
	_, err = New(WithAPIKey("rb_test"), WithBaseURL(c.baseURL), WithMaxRetries(0)).Jobs.Get(context.Background(), "job_t")
	if codeOf(err) != CodeUnknown || statusOf(err) != 502 {
		t.Fatalf("a non-JSON body gave %#v", err)
	}
}

func TestCreatedFlagsAReplay(t *testing.T) {
	n := 0
	c, _ := keyServer(t, func(w http.ResponseWriter, r *http.Request) {
		if n++; n == 1 {
			w.WriteHeader(201)
			fmt.Fprint(w, `{"data":{"id":"job_t","status":"waiting"}}`)
			return
		}
		fmt.Fprint(w, `{"data":{"id":"job_t","status":"running","idempotent":true}}`)
	})
	p := CreateParams{Params: FfprobeParams{Command: "x"}, IdempotencyKey: "mine"}
	first, err1 := c.Jobs.Create(context.Background(), p)
	again, err2 := c.Jobs.Create(context.Background(), p)
	if err1 != nil || err2 != nil || first.Replayed || !again.Replayed || again.ID != first.ID {
		t.Fatalf("%+v %v, %+v %v", first, err1, again, err2)
	}
}

func TestKeyProblemsFailBeforeSending(t *testing.T) {
	for _, tc := range []struct {
		key  string
		code ErrorCode
	}{{"", CodeMissingAPIKey}, {"sk_live_x", CodeInvalidAPIKey}} {
		t.Run(string(tc.code), func(t *testing.T) {
			t.Setenv("RENDOBAR_API_KEY", "")
			var hits atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				hits.Add(1)
				fmt.Fprint(w, `{"data":[]}`)
			}))
			defer srv.Close()
			c := New(WithAPIKey(tc.key), WithBaseURL(srv.URL))
			ctx := context.Background()
			_, getErr := c.Jobs.Get(ctx, "job_t")
			_, createErr := c.Jobs.Create(ctx, CreateParams{Params: FfprobeParams{Command: "x"}})
			_, listErr := c.Jobs.List(ctx, ListParams{})
			_, runErr := c.Jobs.Run(ctx, CreateParams{Params: FfprobeParams{Command: "x"}}, nil)
			_, capsErr := c.Account.Capabilities(ctx)
			_, dlErr := c.Jobs.Download(ctx, "job_t")
			for _, err := range []error{getErr, createErr, listErr, runErr, capsErr, dlErr, c.Jobs.Cancel(ctx, "job_t")} {
				if codeOf(err) != tc.code || !strings.Contains(err.Error(), "RENDOBAR_API_KEY") || !strings.Contains(err.Error(), "WithAPIKey") {
					t.Fatalf("got %v", err)
				}
			}
			if hits.Load() != 0 {
				t.Fatalf("%d requests sent", hits.Load())
			}
			if _, err := c.Jobs.Types(ctx); err != nil || hits.Load() != 1 {
				t.Fatalf("the public endpoint needs no key: %v, %d requests", err, hits.Load())
			}
		})
	}
}

func TestBadIDsAndNegativePagingFailBeforeSending(t *testing.T) {
	c, hits := testServer(t, func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, jobJSON("running", 0)) })
	ctx := context.Background()
	for _, id := range []string{"", "a/b", "..", ".", "job x", "job?x=1", "job_%2F"} {
		_, getErr := c.Jobs.Get(ctx, id)
		_, waitErr := c.Jobs.Wait(ctx, id, nil)
		_, downloadErr := c.Jobs.Download(ctx, id)
		for _, err := range []error{getErr, waitErr, downloadErr, c.Jobs.Cancel(ctx, id)} {
			if codeOf(err) != CodeInvalidArgument {
				t.Fatalf("%q: got %v", id, err)
			}
		}
	}
	for _, p := range []ListParams{{Limit: -1}, {Offset: -1}} {
		if _, err := c.Jobs.List(ctx, p); codeOf(err) != "INVALID_ARGUMENT" {
			t.Fatalf("%+v: got %v", p, err)
		}
	}
	if hits.Load() != 0 {
		t.Fatalf("%d requests sent", hits.Load())
	}
	if _, err := c.Jobs.Get(ctx, "job_A.b-c_1"); err != nil {
		t.Fatalf("a valid id was refused: %v", err)
	}
}

func TestRetrySetAndRetryAfterCap(t *testing.T) {
	for _, tc := range []struct {
		name       string
		status     int
		retryAfter string
		want       int32
	}{
		{"500 retried", 500, "0", 3},
		{"502 retried", 502, "0", 3},
		{"504 retried", 504, "0", 3},
		{"501 not retried", 501, "0", 1},
		{"505 not retried", 505, "0", 1},
		{"Retry-After over 60s stops at once", 429, "3600", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, hits := testServer(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Retry-After", tc.retryAfter)
				w.WriteHeader(tc.status)
				fmt.Fprint(w, `{"error":{"code":"X","message":"no"}}`)
			})
			started := time.Now()
			_, err := c.Jobs.Get(context.Background(), "job_t")
			var apiErr *Error
			if !errors.As(err, &apiErr) || apiErr.StatusCode != tc.status || hits.Load() != tc.want {
				t.Fatalf("got %v after %d requests", err, hits.Load())
			}
			if tc.retryAfter == "3600" && (apiErr.RetryAfter != time.Hour || time.Since(started) > time.Second) {
				t.Fatalf("RetryAfter %v after %v", apiErr.RetryAfter, time.Since(started))
			}
		})
	}
}

func TestRunReportsTheCode(t *testing.T) {
	for status, code := range map[string]string{"failed": "INPUT_UNREADABLE", "cancelled": "JOB_CANCELLED"} {
		c, _ := testServer(t, func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodPost {
				w.WriteHeader(201)
				fmt.Fprint(w, `{"data":{"id":"job_t","status":"waiting"}}`)
				return
			}
			fmt.Fprint(w, jobJSON(status, 0))
		})
		job, err := c.Jobs.Run(context.Background(), CreateParams{Params: FfprobeParams{Command: "x"}}, &WaitOptions{Interval: 10 * time.Millisecond})
		var failed *JobFailedError
		if !errors.As(err, &failed) || failed.Code != code || failed.Job != job {
			t.Fatalf("%s: got %#v", status, err)
		}
	}
}

func TestAllPagesBy100(t *testing.T) {
	var limits []string
	c, _ := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		limits = append(limits, r.URL.Query().Get("limit"))
		fmt.Fprint(w, `{"data":[],"meta":{"total":0}}`)
	})
	for _, limit := range []int{0, 500, 20} {
		for _, err := range c.Jobs.All(context.Background(), ListParams{Limit: limit}) {
			t.Fatal(err)
		}
	}
	if strings.Join(limits, ",") != "100,100,20" {
		t.Fatalf("page sizes %v", limits)
	}
}

func TestABadAppNameFailsEveryCallWithoutPanicking(t *testing.T) {
	c, hits := testServer(t, func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `{"data":[]}`) })
	bad := New(WithAPIKey("rb_test"), WithBaseURL(c.baseURL), WithApp("has spaces"))
	_, getErr := bad.Jobs.Get(context.Background(), "job_t")
	_, typesErr := bad.Jobs.Types(context.Background())
	for _, err := range []error{getErr, typesErr} {
		if codeOf(err) != CodeInvalidArgument || !strings.Contains(err.Error(), "WithApp") {
			t.Fatalf("got %v", err)
		}
	}
	if hits.Load() != 0 {
		t.Fatalf("%d requests sent", hits.Load())
	}
}

func TestLimitsRoundTrip(t *testing.T) {
	in := `{"concurrentJobs":20,"maxQueuedJobs":100,"apiRequestsPerMinute":600,"maxJobTimeout":32400,"maxInputFileSize":10737418240,` +
		`"storageQuota":107374182400,"outputRetentionDays":30,"maxUploadSessionFiles":50,"teamMembers":5,"composeMaxPixels":8294400,` +
		`"composeMaxFps":60,"composeAnimatedMaxPixels":2073600,"composeAnimatedMaxFps":30}`
	var l Limits
	if err := json.Unmarshal([]byte(in), &l); err != nil || l.MaxJobTimeout != 9*time.Hour || l.StorageQuota != 107374182400 {
		t.Fatalf("%+v %v", l, err)
	}
	out, err := json.Marshal(l)
	if err != nil || string(out) != in {
		t.Fatalf("round trip:\n%s\n%s", out, in)
	}
}

func TestSafeFilename(t *testing.T) {
	for in, want := range map[string]string{
		"out.mp4": "out.mp4", "../../etc/passwd": "passwd", `..\..\boot.ini`: "boot.ini", "dir/": "",
		"..": "", "c:evil.exe": "", "a\x00b": "", "": "",
	} {
		if got := safeFilename(in); got != want {
			t.Fatalf("%q: got %q, want %q", in, got, want)
		}
	}
}
