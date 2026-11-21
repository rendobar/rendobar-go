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

// Stop waiting when ctx ends, and keep the job id to pick the job up later.
func ExampleContextError() {
	rb := rendobar.New()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, err := rb.Jobs.Run(ctx, rendobar.CreateParams{
		Type:   "ffprobe",
		Params: map[string]any{"command": "https://cdn.rendobar.com/assets/examples/sample.mp4"},
	}, nil)
	var stopped *rendobar.ContextError
	if errors.As(err, &stopped) {
		fmt.Println("still running:", stopped.JobID, errors.Is(err, context.DeadlineExceeded))
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
