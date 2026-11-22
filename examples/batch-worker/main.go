// Batch-worker submits a batch of jobs the way a queue worker would: each
// with its own idempotency key, so a retried submit never runs a job twice.
// It then walks the error types a worker has to handle, pages through the job
// list, reads a job's logs and timings and the workspace's job counts, and
// cancels a job it no longer needs.
//
//	RENDOBAR_API_KEY=rb_... go run ./batch-worker -mode live
//
// Five jobs: three ffprobe, one ffmpeg that is cancelled and one ffmpeg that fails.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"slices"
	"time"

	"github.com/rendobar/rendobar-go"
)

const sampleURL = "https://cdn.rendobar.com/assets/examples/sample.mp4"

func main() {
	mode := flag.String("mode", "mock", "mock (the conformance mock) or live (a real API)")
	flag.Parse()
	if err := run(*mode); err != nil {
		fmt.Fprintln(os.Stderr, "FAIL:", err)
		os.Exit(1)
	}
	fmt.Println("PASS batch-worker")
}

func run(mode string) error {
	if mode != "mock" && mode != "live" {
		return fmt.Errorf("-mode must be mock or live, got %q", mode)
	}
	mock := mode == "mock"
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()

	var opts []rendobar.Option
	if u := os.Getenv("RENDOBAR_API_URL"); u != "" {
		opts = append(opts, rendobar.WithBaseURL(u))
	}
	rb := rendobar.New(opts...)

	ids, err := submitBatch(ctx, rb)
	if err != nil {
		return err
	}
	if err := checkErrors(ctx, rb, opts, mock); err != nil {
		return err
	}
	for _, id := range ids {
		job, err := rb.Jobs.Wait(ctx, id, nil)
		if err != nil {
			return fmt.Errorf("wait %s: %w", id, err)
		}
		if job.Status() != rendobar.StatusComplete {
			return fmt.Errorf("job %s ended %s, want complete", id, job.Status())
		}
	}
	fmt.Println("batch of", len(ids), "complete")

	if err := inspect(ctx, rb, ids[0], mock); err != nil {
		return err
	}
	if err := page(ctx, rb); err != nil {
		return err
	}
	if err := cancelOne(ctx, rb, mock); err != nil {
		return err
	}
	return failOne(ctx, rb, mock)
}

// submitBatch submits three probes, resubmits one with its key and reuses
// that key with other params.
func submitBatch(ctx context.Context, rb *rendobar.Client) ([]string, error) {
	params := rendobar.FfprobeParams{Command: sampleURL}
	keys := []string{rendobar.GenerateIdempotencyKey(), rendobar.GenerateIdempotencyKey(), rendobar.GenerateIdempotencyKey()}
	var ids []string
	for i, key := range keys {
		created, err := rb.Jobs.Create(ctx, rendobar.CreateParams{
			Params:         params,
			IdempotencyKey: key,
			Label:          fmt.Sprintf("examples/batch-worker %d", i+1),
		})
		if err != nil {
			return nil, fmt.Errorf("submit %d: %w", i+1, err)
		}
		if created.Replayed {
			return nil, fmt.Errorf("submit %d with a new key reports a replay", i+1)
		}
		ids = append(ids, created.ID)
	}
	fmt.Println("submitted", ids)

	// The same key and params: the API returns the job it already created.
	again, err := rb.Jobs.Create(ctx, rendobar.CreateParams{Params: params, IdempotencyKey: keys[0], Label: "examples/batch-worker 1"})
	if err != nil {
		return nil, fmt.Errorf("resubmit: %w", err)
	}
	if again.ID != ids[0] || !again.Replayed {
		return nil, fmt.Errorf("resubmit gave job %s (replayed %v), want %s replayed", again.ID, again.Replayed, ids[0])
	}
	fmt.Println("resubmit returned the same job", again.ID)

	// The same key with other params: a 409 CONFLICT, and no new job.
	_, err = rb.Jobs.Create(ctx, rendobar.CreateParams{
		Params:         rendobar.FfprobeParams{Command: "-show_chapters " + sampleURL},
		IdempotencyKey: keys[0],
	})
	if _, err := wantAPIError(err, rendobar.CodeConflict, 409); err != nil {
		return nil, fmt.Errorf("reused key: %w", err)
	}
	fmt.Println("reused key with other params: 409", rendobar.CodeConflict)
	return ids, nil
}

// checkErrors triggers each error a worker should tell apart.
func checkErrors(ctx context.Context, rb *rendobar.Client, opts []rendobar.Option, mock bool) error {
	// Params that fail their schema are refused before anything is sent.
	_, err := rb.Jobs.Create(ctx, rendobar.CreateParams{Params: rendobar.FfprobeParams{}})
	invalid, err := wantAPIError(err, rendobar.CodeInvalidArgument, 0)
	if err != nil {
		return fmt.Errorf("empty params: %w", err)
	}
	if len(invalid.Problems) != 1 || invalid.Problems[0].Path != "command" {
		return fmt.Errorf("empty params: problems %+v, want one at command", invalid.Problems)
	}
	fmt.Println("empty params:", rendobar.CodeInvalidArgument, "at", invalid.Problems[0].Path)

	// A key that is not a Rendobar key is refused before anything is sent.
	bad := rendobar.New(append(slices.Clip(opts), rendobar.WithAPIKey("not-a-key"))...)
	_, err = bad.Jobs.Get(ctx, "job_404")
	if _, err := wantAPIError(err, rendobar.CodeInvalidAPIKey, 0); err != nil {
		return fmt.Errorf("malformed key: %w", err)
	}
	fmt.Println("malformed key:", rendobar.CodeInvalidAPIKey)

	if mock {
		fmt.Println("SKIP (mock: accepts any bearer key) revoked key gives 401")
	} else {
		revoked := rendobar.New(append(slices.Clip(opts), rendobar.WithAPIKey("rb_examples_not_a_real_key"))...)
		_, err = revoked.Jobs.Get(ctx, "job_404")
		if _, err := wantAPIError(err, rendobar.CodeUnauthorized, 401); err != nil {
			return fmt.Errorf("unknown key: %w", err)
		}
		fmt.Println("unknown key: 401", rendobar.CodeUnauthorized)
	}

	_, err = rb.Jobs.Get(ctx, "job_404")
	notFound, err := wantAPIError(err, rendobar.CodeNotFound, 404)
	if err != nil {
		return fmt.Errorf("missing job: %w", err)
	}
	if notFound.RequestID == "" {
		return errors.New("missing job: the error has no request ID")
	}
	fmt.Println("missing job: 404", rendobar.CodeNotFound, "request", notFound.RequestID)

	// A submit cut off by its context may have created a job: SubmitError
	// carries the key to submit again with.
	stopped, stop := context.WithCancel(ctx)
	stop()
	_, err = rb.Jobs.Create(stopped, rendobar.CreateParams{Params: rendobar.FfprobeParams{Command: sampleURL}})
	var submitErr *rendobar.SubmitError
	if !errors.As(err, &submitErr) || submitErr.IdempotencyKey == "" || !errors.Is(err, context.Canceled) {
		return fmt.Errorf("cancelled submit: got %v, want a *SubmitError with a key wrapping context.Canceled", err)
	}
	fmt.Println("cancelled submit: SubmitError with key", submitErr.IdempotencyKey)
	return nil
}

// inspect reads what a worker logs when a job needs a closer look: its log
// entries, how long each stage took, and the workspace's counts for the day.
func inspect(ctx context.Context, rb *rendobar.Client, id string, mock bool) error {
	logs, err := rb.Jobs.Logs(ctx, id)
	if err != nil {
		return fmt.Errorf("logs %s: %w", id, err)
	}
	if len(logs) == 0 {
		return fmt.Errorf("job %s has no log entries", id)
	}
	for _, e := range logs {
		if e.Message == "" || e.Timestamp.Time().IsZero() {
			return fmt.Errorf("log entry %+v has no message or time", e)
		}
	}
	fmt.Printf("job %s logged %d entries, first %q\n", id, len(logs), logs[0].Message)

	timings, err := rb.Jobs.Timings(ctx, id)
	if err != nil {
		return fmt.Errorf("timings %s: %w", id, err)
	}
	if !mock && timings.JobID != id {
		return fmt.Errorf("timings are for job %s, want %s", timings.JobID, id)
	}
	if timings.APITotalMs != nil {
		fmt.Println("the API took", time.Duration(*timings.APITotalMs)*time.Millisecond, "to accept it")
	}

	stats, err := rb.Jobs.Stats(ctx, rendobar.StatsParams{Window: rendobar.StatsWindowDay})
	if err != nil {
		return fmt.Errorf("stats: %w", err)
	}
	if stats.Totals == nil || stats.Totals.Jobs < 1 {
		return fmt.Errorf("stats count no jobs today: %+v", stats.Totals)
	}
	fmt.Println("jobs in the last 24 hours:", stats.Totals.Jobs, "succeeded:", stats.Totals.Succeeded)
	return nil
}

// page walks the job list two at a time and stops after six jobs.
func page(ctx context.Context, rb *rendobar.Client) error {
	first, err := rb.Jobs.List(ctx, rendobar.ListParams{Limit: 2})
	if err != nil {
		return fmt.Errorf("list: %w", err)
	}
	if len(first.Jobs) != 2 || first.Total < 6 {
		return fmt.Errorf("first page has %d jobs of %d, want 2 of at least 6", len(first.Jobs), first.Total)
	}
	seen := map[string]bool{}
	for job, err := range rb.Jobs.All(ctx, rendobar.ListParams{Limit: 2}) {
		if err != nil {
			return fmt.Errorf("list all: %w", err)
		}
		if job.ID() == "" || job.Status() == "" || seen[job.ID()] {
			return fmt.Errorf("list all: job %q status %q (seen before: %v)", job.ID(), job.Status(), seen[job.ID()])
		}
		seen[job.ID()] = true
		if len(seen) == 6 {
			break
		}
	}
	if len(seen) != 6 {
		return fmt.Errorf("list all gave %d jobs, want 6", len(seen))
	}
	fmt.Println("paged 6 jobs of", first.Total, "two at a time")
	return nil
}

// cancelOne starts a job, stops waiting on it twice and then cancels it.
func cancelOne(ctx context.Context, rb *rendobar.Client, mock bool) error {
	created, err := rb.Jobs.Create(ctx, rendobar.CreateParams{
		Params:     rendobar.FfmpegParams{Command: "ffmpeg -i " + sampleURL + " -t 2 -c:v libx264 cancel-me.mp4"},
		Label:      "examples/batch-worker cancel",
		ForceAsync: true,
	})
	if err != nil {
		return fmt.Errorf("submit the job to cancel: %w", err)
	}

	// Wait's own deadline passes, here before the first poll returns: the job is still going.
	_, err = rb.Jobs.Wait(ctx, created.ID, &rendobar.WaitOptions{Timeout: time.Nanosecond})
	var timeout *rendobar.WaitTimeoutError
	if !errors.As(err, &timeout) || timeout.JobID != created.ID {
		return fmt.Errorf("wait timeout: got %v, want a *WaitTimeoutError for %s", err, created.ID)
	}
	fmt.Println("wait timeout: job", timeout.JobID, "still", timeout.LastStatus)

	// The caller's context ends: the job keeps running and ContextError names it.
	expired, stop := context.WithTimeout(ctx, time.Nanosecond)
	defer stop()
	<-expired.Done()
	_, err = rb.Jobs.Wait(expired, created.ID, nil)
	var ctxErr *rendobar.ContextError
	if !errors.As(err, &ctxErr) || ctxErr.JobID != created.ID || !errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("expired wait: got %v, want a *ContextError for %s", err, created.ID)
	}
	fmt.Println("expired wait: ContextError for", ctxErr.JobID)

	if err := rb.Jobs.Cancel(ctx, created.ID); err != nil {
		return fmt.Errorf("cancel: %w", err)
	}
	if mock {
		fmt.Println("SKIP (mock: a created job always completes) cancelled job ends cancelled")
		return nil
	}
	job, err := rb.Jobs.Wait(ctx, created.ID, nil)
	if err != nil {
		return fmt.Errorf("wait for the cancel: %w", err)
	}
	if job.Status() != rendobar.StatusCancelled || job.Cancelled == nil {
		return fmt.Errorf("job %s ended %s after the cancel, want cancelled", job.ID(), job.Status())
	}
	fmt.Println("cancelled", job.ID())
	return nil
}

// failOne runs a job whose command fails, and reads why.
func failOne(ctx context.Context, rb *rendobar.Client, mock bool) error {
	if mock {
		// A created job always completes in the mock, so read its failed job instead.
		job, err := rb.Jobs.Wait(ctx, "job_failed", nil)
		if err != nil {
			return fmt.Errorf("wait job_failed: %w", err)
		}
		if job.Failed == nil || job.Failed.Error.Code == "" {
			return fmt.Errorf("job_failed ended %s with no error code", job.Status())
		}
		fmt.Println("failed job:", job.Failed.Error.Code)
		fmt.Println("SKIP (mock: a created job always completes) Run returns JobFailedError")
		return nil
	}
	// The sample has no subtitle stream, so mapping one fails the job.
	job, err := rb.Jobs.Run(ctx, rendobar.CreateParams{
		Params: rendobar.FfmpegParams{Command: "ffmpeg -i " + sampleURL + " -map 0:s fail.mp4"},
		Label:  "examples/batch-worker fail",
	}, nil)
	var failed *rendobar.JobFailedError
	if !errors.As(err, &failed) || failed.Code == "" || job == nil || job.Failed == nil {
		return fmt.Errorf("failing job: got %v, want a *JobFailedError with a code", err)
	}
	fmt.Println("failed job:", failed.Code, "-", job.Failed.Error.Message)
	return nil
}

// wantAPIError returns err as an *Error when it has code and HTTP status
// (0 for an error the SDK raised before sending anything).
func wantAPIError(err error, code rendobar.ErrorCode, status int) (*rendobar.Error, error) {
	var apiErr *rendobar.Error
	if !errors.As(err, &apiErr) {
		return nil, fmt.Errorf("got %v, want *rendobar.Error %s", err, code)
	}
	if apiErr.Code != code || apiErr.StatusCode != status {
		return nil, fmt.Errorf("got %s %d, want %s %d", apiErr.Code, apiErr.StatusCode, code, status)
	}
	return apiErr, nil
}
