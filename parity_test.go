package rendobar

// Shared conformance scenarios C19 to C39 (rendobar/openapi conformance/SCENARIOS.md).
// They need a mock that serves /__requests. Against an older mock they skip.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"
)

type mockRequest struct {
	Method string `json:"method"`
	Path   string `json:"path"`
	Query  string `json:"query"`
}

// parity resets the mock with query and skips when the mock predates these scenarios.
func parity(t *testing.T, query string) {
	t.Helper()
	res, err := http.Get(mockURL() + "/__requests")
	if err != nil {
		t.Fatalf("conformance mock not reachable: %v", err)
	}
	res.Body.Close()
	if res.StatusCode == http.StatusNotFound {
		t.Skip("the conformance mock predates scenarios C19 to C39")
	}
	reset(t, query)
}

func requests(t *testing.T) []mockRequest {
	t.Helper()
	res, err := http.Get(mockURL() + "/__requests")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var out struct {
		Data []mockRequest `json:"data"`
	}
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	return out.Data
}

func createdCount(t *testing.T) int {
	t.Helper()
	res, err := http.Get(mockURL() + "/__created")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var out struct {
		Data struct {
			Count int `json:"count"`
		} `json:"data"`
	}
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	return out.Data.Count
}

func TestC19MissingKeyFailsLocally(t *testing.T) {
	parity(t, "")
	t.Setenv("RENDOBAR_API_KEY", "")
	_, err := New(WithBaseURL(mockURL())).Jobs.Get(context.Background(), "job_x")
	if codeOf(err) != "MISSING_API_KEY" || len(requests(t)) != 0 {
		t.Fatalf("got %v, %d requests", err, len(requests(t)))
	}
}

func TestC20NonRendobarKeyFailsLocally(t *testing.T) {
	parity(t, "")
	_, err := New(WithAPIKey("sk_x"), WithBaseURL(mockURL())).Jobs.Get(context.Background(), "job_x")
	if codeOf(err) != "INVALID_API_KEY" || len(requests(t)) != 0 {
		t.Fatalf("got %v, %d requests", err, len(requests(t)))
	}
}

func TestC21TrailingSlashBaseURL(t *testing.T) {
	parity(t, "")
	c := New(WithAPIKey("rb_conformance"), WithBaseURL(mockURL()+"/"))
	if _, err := c.Jobs.Get(context.Background(), "job_x"); err != nil {
		t.Fatal(err)
	}
	reqs := requests(t)
	if len(reqs) != 1 || reqs[0].Path != "/jobs/job_x" {
		t.Fatalf("requests %+v", reqs)
	}
}

func TestC22CutOffSubmitCarriesTheKey(t *testing.T) {
	parity(t, "?stall_submit=1")
	p := CreateParams{Type: "ffprobe", Params: map[string]any{"command": "https://example.com/a.mp4"}}
	_, err := client(WithTimeout(2*time.Second)).Jobs.Create(context.Background(), p)
	var se *SubmitError
	if !errors.As(err, &se) || se.IdempotencyKey == "" {
		t.Fatalf("got %v", err)
	}
	p.IdempotencyKey = se.IdempotencyKey
	again, err := client().Jobs.Create(context.Background(), p)
	if err != nil || !again.Replayed || createdCount(t) != 1 {
		t.Fatalf("resubmit %+v, %v, created %d", again, err, createdCount(t))
	}
}

func TestC23SameKeyDifferentParamsIsAConflict(t *testing.T) {
	parity(t, "")
	c := client()
	p := CreateParams{Type: "ffprobe", Params: map[string]any{"command": "a"}, IdempotencyKey: "key_c23"}
	if _, err := c.Jobs.Create(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	p.Params = map[string]any{"command": "b"}
	_, err := c.Jobs.Create(context.Background(), p)
	if !IsConflict(err) || codeOf(err) != "CONFLICT" || len(requests(t)) != 2 {
		t.Fatalf("got %v after %d requests", err, len(requests(t)))
	}
}

func TestC24SameKeySameParamsIsAReplay(t *testing.T) {
	parity(t, "")
	c := client()
	p := CreateParams{Type: "ffprobe", Params: map[string]any{"command": "a"}, IdempotencyKey: "key_c24"}
	first, err1 := c.Jobs.Create(context.Background(), p)
	again, err2 := c.Jobs.Create(context.Background(), p)
	if err1 != nil || err2 != nil || first.Replayed || !again.Replayed || again.ID != first.ID {
		t.Fatalf("%+v %v, %+v %v", first, err1, again, err2)
	}
}

func waited(t *testing.T, id string, least time.Duration) {
	t.Helper()
	started := time.Now()
	if _, err := client().Jobs.Get(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	if d := time.Since(started); d < least || d > 5*time.Second {
		t.Fatalf("%s took %v", id, d)
	}
	if n := len(requests(t)); n != 2 {
		t.Fatalf("%d requests, want 2", n)
	}
}

func TestC25RetryAfterOnA503(t *testing.T) {
	parity(t, "")
	waited(t, "job_503_ra", 900*time.Millisecond)
}

func TestC26LongRetryAfterIsNotWaitedOut(t *testing.T) {
	parity(t, "")
	started := time.Now()
	_, err := client().Jobs.Get(context.Background(), "job_ra_long")
	var apiErr *Error
	if !errors.As(err, &apiErr) || apiErr.RetryAfter != time.Hour || len(requests(t)) != 1 || time.Since(started) > 2*time.Second {
		t.Fatalf("got %v after %d requests", err, len(requests(t)))
	}
}

func TestC27RetryAfterAsAnHTTPDate(t *testing.T) {
	parity(t, "")
	waited(t, "job_ra_date", 500*time.Millisecond)
}

func TestC28A501IsNotRetried(t *testing.T) {
	parity(t, "")
	_, err := client().Jobs.Get(context.Background(), "job_501")
	var apiErr *Error
	if !errors.As(err, &apiErr) || apiErr.StatusCode != 501 || len(requests(t)) != 1 {
		t.Fatalf("got %v after %d requests", err, len(requests(t)))
	}
}

func TestC29AllWithALargePageSize(t *testing.T) {
	parity(t, "")
	n := 0
	for _, err := range client().Jobs.All(context.Background(), ListParams{Limit: 500}) {
		if err != nil {
			t.Fatal(err)
		}
		n++
	}
	var offsets []string
	for _, r := range requests(t) {
		offsets = append(offsets, r.Query)
	}
	if n != 120 || len(offsets) != 2 || !strings.Contains(offsets[1], "offset=100") {
		t.Fatalf("%d jobs over %v", n, offsets)
	}
}

func TestC30AllStopsOnAnEmptyPage(t *testing.T) {
	parity(t, "?phantom_total=1")
	n := 0
	for _, err := range client().Jobs.All(context.Background(), ListParams{}) {
		if err != nil {
			t.Fatal(err)
		}
		n++
	}
	if n != 60 || len(requests(t)) > 2 {
		t.Fatalf("%d jobs over %d requests", n, len(requests(t)))
	}
}

func TestC31NegativePagingFailsLocally(t *testing.T) {
	parity(t, "")
	for _, p := range []ListParams{{Limit: -1}, {Offset: -1}} {
		if _, err := client().Jobs.List(context.Background(), p); codeOf(err) != "INVALID_ARGUMENT" {
			t.Fatalf("%+v: got %v", p, err)
		}
	}
	if n := len(requests(t)); n != 0 {
		t.Fatalf("%d requests", n)
	}
}

func TestC32BadIDsFailLocally(t *testing.T) {
	parity(t, "")
	for _, id := range []string{"", "a/b", ".."} {
		if _, err := client().Jobs.Get(context.Background(), id); codeOf(err) != "INVALID_ARGUMENT" {
			t.Fatalf("%q: got %v", id, err)
		}
	}
	if n := len(requests(t)); n != 0 {
		t.Fatalf("%d requests", n)
	}
}

func TestC33ValidationProblems(t *testing.T) {
	parity(t, "")
	_, err := client().Jobs.Create(context.Background(), CreateParams{Type: "no_such_type"})
	var apiErr *Error
	if !errors.As(err, &apiErr) || apiErr.Code != "VALIDATION_ERROR" || len(apiErr.Problems) == 0 ||
		apiErr.Problems[0].Path == "" || apiErr.Problems[0].Message == "" {
		t.Fatalf("got %#v", err)
	}
}

func TestC34WaitTimeoutCarriesTheLastJob(t *testing.T) {
	parity(t, "")
	_, err := client().Jobs.Wait(context.Background(), "job_stuck", &WaitOptions{Timeout: 3 * time.Second, Interval: 500 * time.Millisecond})
	var te *WaitTimeoutError
	if !errors.As(err, &te) || te.JobID != "job_stuck" || te.LastStatus != StatusRunning || te.Job == nil || te.Job.ID() != "job_stuck" {
		t.Fatalf("got %#v", err)
	}
}

func TestC35WaitReturnsACancelledJobAndRunRaises(t *testing.T) {
	parity(t, "?cancel_created=1")
	job, err := client().Jobs.Wait(context.Background(), "job_cancelled", nil)
	if err != nil || job.Status() != StatusCancelled || job.Cancelled == nil {
		t.Fatalf("wait: %v, %v", job, err)
	}
	_, err = client().Jobs.Run(context.Background(), CreateParams{Type: "ffprobe", Params: map[string]any{"command": "a"}},
		&WaitOptions{Interval: 100 * time.Millisecond})
	var failed *JobFailedError
	if !errors.As(err, &failed) || failed.Code != "JOB_CANCELLED" || failed.Job == nil {
		t.Fatalf("run: %#v", err)
	}
}

func TestC36ATimedOutAttemptIsNotRetried(t *testing.T) {
	parity(t, "")
	if _, err := client(WithTimeout(2*time.Second)).Jobs.Get(context.Background(), "job_slow"); err == nil {
		t.Fatal("no timeout")
	}
	if n := len(requests(t)); n != 1 {
		t.Fatalf("%d requests, want 1", n)
	}
}

func TestC37WaitOnAnUnknownStatus(t *testing.T) {
	parity(t, "")
	_, err := client().Jobs.Wait(context.Background(), "job_future", &WaitOptions{Timeout: 3 * time.Second, Interval: 500 * time.Millisecond})
	var te *WaitTimeoutError
	if !errors.As(err, &te) || te.LastStatus != "paused" {
		t.Fatalf("got %#v", err)
	}
}

func TestC38ClientHeaderMatchesTheManifest(t *testing.T) {
	parity(t, "")
	raw, err := os.ReadFile(".release-please-manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	var manifest map[string]string
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatal(err)
	}
	rec := &recorder{}
	c := client(WithHTTPClient(&http.Client{Transport: rec}), WithApp("c38-app"))
	if _, err := c.Jobs.Get(context.Background(), "job_x"); err != nil {
		t.Fatal(err)
	}
	want := regexp.QuoteMeta("go/"+manifest["."]+" (go/"+strings.TrimPrefix(runtime.Version(), "go")) + `; app/c38-app\)`
	if got := rec.reqs[0].header.Get("X-Rendobar-Client"); manifest["."] != Version || !regexp.MustCompile("^"+want+"$").MatchString(got) {
		t.Fatalf("header %q, manifest %q, Version %q", got, manifest["."], Version)
	}
}
