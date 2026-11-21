package rendobar

// The shared SDK conformance scenarios (rendobar/openapi conformance/SCENARIOS.md),
// against the conformance mock on RENDOBAR_MOCK (default http://127.0.0.1:4100).

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

func mockURL() string {
	if v := os.Getenv("RENDOBAR_MOCK"); v != "" {
		return v
	}
	return "http://127.0.0.1:4100"
}

func reset(t *testing.T, query string) {
	t.Helper()
	res, err := http.Post(mockURL()+"/__reset"+query, "application/json", nil)
	if err != nil {
		t.Fatalf("conformance mock not reachable: %v", err)
	}
	res.Body.Close()
}

// recorder captures what the SDK sends. The network itself is real.
type recorder struct {
	mu   sync.Mutex
	reqs []recorded
}

type recorded struct {
	method, path string
	header       http.Header
	body         []byte
}

func (r *recorder) RoundTrip(req *http.Request) (*http.Response, error) {
	var body []byte
	if req.Body != nil {
		body, _ = io.ReadAll(req.Body)
		req.Body = io.NopCloser(strings.NewReader(string(body)))
	}
	r.mu.Lock()
	r.reqs = append(r.reqs, recorded{req.Method, req.URL.Path, req.Header.Clone(), body})
	r.mu.Unlock()
	return http.DefaultTransport.RoundTrip(req)
}

func client(opts ...Option) *Client {
	return New(append([]Option{WithAPIKey("rb_conformance"), WithBaseURL(mockURL())}, opts...)...)
}

func TestC01PublicEndpointWithoutAKey(t *testing.T) {
	reset(t, "")
	t.Setenv("RENDOBAR_API_KEY", "")
	types, err := New(WithBaseURL(mockURL())).Jobs.Types(context.Background())
	if err != nil || len(types) == 0 {
		t.Fatalf("types: %v, %d", err, len(types))
	}
}

func TestC02GetUnwrapsTheJob(t *testing.T) {
	reset(t, "")
	job, err := client().Jobs.Get(context.Background(), "job_x")
	if err != nil {
		t.Fatal(err)
	}
	if job.Status() == "" {
		t.Fatal("no status")
	}
}

func TestC03CreateReturnsTheJobID(t *testing.T) {
	reset(t, "")
	created, err := client().Jobs.Create(context.Background(), CreateParams{Type: "ffprobe", Params: map[string]any{"command": "https://x/a.mp4"}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(created.ID, "job_") {
		t.Fatalf("id %q", created.ID)
	}
}

func TestC04AllPagesThroughEveryJob(t *testing.T) {
	reset(t, "")
	n := 0
	for _, err := range client().Jobs.All(context.Background(), ListParams{Limit: 50}) {
		if err != nil {
			t.Fatal(err)
		}
		n++
	}
	if n != 120 {
		t.Fatalf("got %d jobs, want 120", n)
	}
}

func TestC05TypedNotFound(t *testing.T) {
	reset(t, "")
	_, err := client().Jobs.Get(context.Background(), "job_404")
	var apiErr *Error
	if !errors.As(err, &apiErr) || apiErr.Code != "NOT_FOUND" || apiErr.StatusCode != 404 || !IsNotFound(err) {
		t.Fatalf("got %v", err)
	}
}

func TestC06RetriesAfterRetryAfter(t *testing.T) {
	reset(t, "")
	started := time.Now()
	job, err := client().Jobs.Get(context.Background(), "job_flaky")
	if err != nil {
		t.Fatal(err)
	}
	if d := time.Since(started); d < 900*time.Millisecond || d > 3*time.Second {
		t.Fatalf("waited %v, want about 1s", d)
	}
	if job.Status() == "" {
		t.Fatal("no job")
	}
}

func TestC07Retries503ThenFails(t *testing.T) {
	reset(t, "")
	_, err := client().Jobs.Get(context.Background(), "job_503")
	var apiErr *Error
	if !errors.As(err, &apiErr) || apiErr.StatusCode != 503 {
		t.Fatalf("got %v", err)
	}
}

func TestC08Timeout(t *testing.T) {
	reset(t, "")
	started := time.Now()
	_, err := client(WithTimeout(2*time.Second), WithMaxRetries(0)).Jobs.Get(context.Background(), "job_slow")
	if err == nil {
		t.Fatal("no error")
	}
	if d := time.Since(started); d < 1800*time.Millisecond || d > 4*time.Second {
		t.Fatalf("failed after %v, want about 2s", d)
	}
}

func TestC09UnknownStatusDoesNotFail(t *testing.T) {
	reset(t, "")
	job, err := client().Jobs.Get(context.Background(), "job_future")
	if err != nil {
		t.Fatal(err)
	}
	if job.Status() != "paused" {
		t.Fatalf("status %q", job.Status())
	}
}

func TestC10DownloadReturnsTheBytes(t *testing.T) {
	reset(t, "")
	b, err := client().Jobs.Download(context.Background(), "job_x")
	if err != nil {
		t.Fatal(err)
	}
	if len(b) != 1024 {
		t.Fatalf("%d bytes", len(b))
	}
}

func TestC11EveryRequestNamesThisSDK(t *testing.T) {
	reset(t, "")
	rec := &recorder{}
	c := client(WithHTTPClient(&http.Client{Transport: rec}))
	if _, err := c.Jobs.Get(context.Background(), "job_x"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Jobs.Types(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, r := range rec.reqs {
		if h := r.header.Get("X-Rendobar-Client"); !strings.HasPrefix(h, "go/"+Version+" (go/") {
			t.Fatalf("header %q", h)
		}
	}
}

func TestC12ErrorCarriesTheRequestID(t *testing.T) {
	reset(t, "")
	_, err := client().Jobs.Get(context.Background(), "job_404")
	var apiErr *Error
	if !errors.As(err, &apiErr) || !strings.HasPrefix(apiErr.RequestID, "req_") {
		t.Fatalf("got %v", err)
	}
}

func TestC13RetriedSubmitCreatesOneJob(t *testing.T) {
	reset(t, "?lose_submit=1")
	rec := &recorder{}
	c := client(WithHTTPClient(&http.Client{Transport: rec}))
	if _, err := c.Jobs.Create(context.Background(), CreateParams{Type: "ffprobe", Params: map[string]any{"command": "https://x/a.mp4"}}); err != nil {
		t.Fatal(err)
	}
	var keys []string
	for _, r := range rec.reqs {
		if r.path == "/jobs" {
			var b struct {
				IdempotencyKey string `json:"idempotencyKey"`
			}
			_ = json.Unmarshal(r.body, &b)
			keys = append(keys, b.IdempotencyKey)
		}
	}
	if len(keys) != 2 || keys[0] != keys[1] || !strings.HasPrefix(keys[0], "sdk_") {
		t.Fatalf("keys %v", keys)
	}
	res, err := http.Get(mockURL() + "/__created")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var created struct {
		Data struct {
			Count int `json:"count"`
		} `json:"data"`
	}
	_ = json.NewDecoder(res.Body).Decode(&created)
	if created.Data.Count != 1 {
		t.Fatalf("%d jobs created, want 1", created.Data.Count)
	}
}

func TestC14DeprecatedEndpointReportedOnce(t *testing.T) {
	reset(t, "")
	var notices []DeprecationNotice
	c := client(WithDeprecationHandler(func(n DeprecationNotice) { notices = append(notices, n) }))
	if _, err := c.Jobs.Get(context.Background(), "job_x"); err != nil {
		t.Fatal(err)
	}
	if len(notices) != 0 {
		t.Fatalf("current endpoint reported: %v", notices)
	}
	// No public Go method maps to a deprecated route, so call one through the core.
	for i := 0; i < 2; i++ {
		_, _ = c.core.Billing.CreateProCheckout(context.Background())
	}
	if len(notices) != 1 || notices[0].Path != "/billing/checkout/pro" || notices[0].DeprecatedAt.Format("2006-01-02") != "2026-10-17" {
		t.Fatalf("notices %+v", notices)
	}
}

func TestC15ReadsTheKeyFromTheEnvironment(t *testing.T) {
	reset(t, "")
	t.Setenv("RENDOBAR_API_KEY", "rb_from_env")
	rec := &recorder{}
	c := New(WithBaseURL(mockURL()), WithHTTPClient(&http.Client{Transport: rec}))
	if _, err := c.Jobs.Get(context.Background(), "job_x"); err != nil {
		t.Fatal(err)
	}
	if got := rec.reqs[0].header.Get("Authorization"); got != "Bearer rb_from_env" {
		t.Fatalf("authorization %q", got)
	}
}

func TestDeprecationHandlerThatPanicsDoesNotBreakTheCall(t *testing.T) {
	reset(t, "")
	c := client(WithDeprecationHandler(func(DeprecationNotice) { panic("logger down") }))
	if _, err := c.core.Billing.CreateProCheckout(context.Background()); err != nil {
		var apiErr *Error
		if errors.As(fromGenerated(err), &apiErr) {
			return // the call completed; the API's own answer is fine
		}
		t.Fatalf("call broke: %v", err)
	}
}

func TestC16RunCompletes(t *testing.T) {
	reset(t, "")
	var seen []string
	job, err := client().Jobs.Run(context.Background(), CreateParams{Type: "ffprobe", Params: map[string]any{"command": "https://x/a.mp4"}},
		&WaitOptions{Interval: 20 * time.Millisecond, OnProgress: func(j *Job) { seen = append(seen, string(j.Status())) }})
	if err != nil {
		t.Fatal(err)
	}
	if job.Status() != StatusComplete || job.Complete == nil || job.Complete.Output.File == nil || job.Complete.Output.File.URL == "" {
		t.Fatalf("job %+v", job)
	}
	if strings.Join(seen, ",") != "running,complete" {
		t.Fatalf("OnProgress saw %v, want running then complete", seen)
	}
}

func TestC17WaitOnAFailedJob(t *testing.T) {
	reset(t, "")
	job, err := client().Jobs.Wait(context.Background(), "job_failed", &WaitOptions{Interval: 20 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	if job.Status() != StatusFailed || job.Failed == nil || job.Failed.Error.Code == "" {
		t.Fatalf("job %+v", job)
	}
	failed := &JobFailedError{Job: job}
	if !strings.Contains(failed.Error(), job.Failed.Error.Code) {
		t.Fatalf("error %q does not carry the code %q", failed.Error(), job.Failed.Error.Code)
	}
}

func TestC18ConflictIsNotRetried(t *testing.T) {
	reset(t, "")
	rec := &recorder{}
	_, err := client(WithHTTPClient(&http.Client{Transport: rec})).Jobs.Get(context.Background(), "job_409")
	var apiErr *Error
	if !errors.As(err, &apiErr) || apiErr.StatusCode != 409 || apiErr.Code != "CONFLICT" {
		t.Fatalf("got %v", err)
	}
	if len(rec.reqs) != 1 {
		t.Fatalf("%d requests, want exactly 1", len(rec.reqs))
	}
}
