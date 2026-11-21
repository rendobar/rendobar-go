package rendobar_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/rendobar/rendobar-go"
	"github.com/rendobar/rendobar-go/webhooks"
)

// Run an ffmpeg job and read the URL of the file it wrote.
func Example() {
	rb := rendobar.New() // reads RENDOBAR_API_KEY
	job, err := rb.Jobs.Run(context.Background(), rendobar.CreateParams{
		Type:   "ffmpeg",
		Params: map[string]any{"command": "ffmpeg -i https://cdn.rendobar.com/assets/examples/sample.mp4 -vf scale=1280:-2 out.mp4"},
	}, nil)
	if err != nil {
		var failed *rendobar.JobFailedError
		if errors.As(err, &failed) {
			fmt.Println("job failed:", failed)
		}
		return
	}
	fmt.Println(job.Complete.Output.File.URL)
}

// Probe a file. ffprobe returns data and writes no file, so read Output.Data.
func Example_probe() {
	rb := rendobar.New()
	job, err := rb.Jobs.Run(context.Background(), rendobar.CreateParams{
		Type:   "ffprobe",
		Params: map[string]any{"command": "https://cdn.rendobar.com/assets/examples/sample.mp4"},
	}, nil)
	if err != nil {
		fmt.Println(err)
		return
	}
	fmt.Println(job.Complete.Output.Data)
}

// Wait for a job you submitted earlier. Wait returns a failed or cancelled
// job with a nil error, so check its status before reading the output.
func ExampleJobs_Wait() {
	rb := rendobar.New()
	job, err := rb.Jobs.Wait(context.Background(), "job_abc123", nil)
	if err != nil {
		fmt.Println(err)
		return
	}
	if job.Status() != rendobar.StatusComplete {
		fmt.Println("job ended", job.Status())
		return
	}
	fmt.Println(job.Complete.Output.File.URL)
}

// List recent jobs. The accessors read what every status shares.
func ExampleJobs_All() {
	rb := rendobar.New()
	for job, err := range rb.Jobs.All(context.Background(), rendobar.ListParams{Limit: 20}) {
		if err != nil {
			fmt.Println(err)
			return
		}
		fmt.Println(job.ID(), job.Type(), job.Status(), job.CreatedAt().Format(time.RFC3339))
	}
}

// Stop when ctx ends, and keep the job id or the idempotency key to pick the job up later.
func ExampleContextError() {
	rb := rendobar.New()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	params := rendobar.CreateParams{
		Type:   "ffprobe",
		Params: map[string]any{"command": "https://cdn.rendobar.com/assets/examples/sample.mp4"},
	}
	_, err := rb.Jobs.Run(ctx, params, nil)
	var stopped *rendobar.ContextError
	var cutOff *rendobar.SubmitError
	switch {
	case errors.As(err, &stopped):
		// Submitted: the job keeps running. Pick it up later by id.
		fmt.Println("still running:", stopped.JobID, errors.Is(err, context.DeadlineExceeded))
	case errors.As(err, &cutOff):
		// The submit itself was cut off. Resubmitting with the same key returns
		// the job the API may have created, never a second one.
		params.IdempotencyKey = cutOff.IdempotencyKey
		created, err := rb.Jobs.Create(context.Background(), params)
		fmt.Println(created, err)
	}
}

// Tell a missing job from other errors, and quote the request id to support.
func ExampleError() {
	rb := rendobar.New()
	_, err := rb.Jobs.Get(context.Background(), "job_missing")
	var apiErr *rendobar.Error
	if errors.As(err, &apiErr) {
		fmt.Println(apiErr.Code, apiErr.StatusCode, apiErr.RequestID)
	}
}

// Verify a webhook delivery in a net/http handler.
func Example_verifyWebhook() {
	secret := os.Getenv("RENDOBAR_WEBHOOK_SECRET")
	http.HandleFunc("/rendobar-webhook", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body) // the exact bytes the signature covers
		if !webhooks.Verify(body, r.Header, secret) {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
}
