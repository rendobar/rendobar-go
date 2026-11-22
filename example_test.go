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

const sample = "https://cdn.rendobar.com/assets/examples/sample.mp4"

// Run an ffmpeg job and read the URL of the file it wrote.
func Example() {
	rb := rendobar.New() // reads RENDOBAR_API_KEY
	job, err := rb.Jobs.Run(context.Background(), rendobar.CreateParams{
		Params: rendobar.FfmpegParams{Command: "ffmpeg -i " + sample + " -vf scale=1280:-2 out.mp4"},
	}, nil)
	if err != nil {
		var failed *rendobar.JobFailedError
		if errors.As(err, &failed) {
			fmt.Println("job failed:", failed.Code)
		}
		return
	}
	fmt.Println(job.Complete.Output.File.URL)
}

// Probe a file. ffprobe returns data and writes no file, so read it with Ffprobe.
func Example_probe() {
	rb := rendobar.New()
	job, err := rb.Jobs.Run(context.Background(), rendobar.CreateParams{
		Params: rendobar.FfprobeParams{Command: sample},
	}, nil)
	if err != nil {
		fmt.Println(err)
		return
	}
	probe, err := job.Complete.Ffprobe()
	if err != nil {
		fmt.Println(err)
		return
	}
	// The summary holds one variant per kind of media. A kind newer than this
	// SDK sets Kind and leaves every variant nil.
	if s := probe.Summary; s != nil && s.Video != nil && s.Video.Video != nil {
		v := s.Video.Video
		fmt.Println(v.Codec, *v.Width, *v.Height)
	}
}

// Submit a job type this SDK predates. CustomParams is the one way to do it.
func ExampleCustomParams() {
	rb := rendobar.New()
	created, err := rb.Jobs.Create(context.Background(), rendobar.CreateParams{
		Params: rendobar.CustomParams{Type: "compose", Fields: map[string]any{"timeline": map[string]any{}}},
	})
	fmt.Println(created, err)
}

// Stream an output file to disk. Close the body when done.
func ExampleJobs_Download() {
	rb := rendobar.New()
	d, err := rb.Jobs.Download(context.Background(), "job_abc123")
	if err != nil {
		fmt.Println(err)
		return
	}
	defer d.Body.Close()
	f, err := os.Create("out.mp4")
	if err != nil {
		fmt.Println(err)
		return
	}
	defer f.Close()
	n, err := io.Copy(f, d.Body)
	fmt.Println(n, err)
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
	params := rendobar.CreateParams{Params: rendobar.FfprobeParams{Command: sample}}
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

// Check an error by its code, and quote the request id to support.
func ExampleError() {
	rb := rendobar.New()
	_, err := rb.Jobs.Get(context.Background(), "job_missing")
	var apiErr *rendobar.Error
	if errors.As(err, &apiErr) {
		switch apiErr.Code {
		case rendobar.CodeNotFound:
			fmt.Println("no such job")
		case rendobar.CodeRateLimited:
			fmt.Println("retry after", apiErr.RetryAfter)
		default:
			fmt.Println(apiErr.Code, apiErr.StatusCode, apiErr.RequestID)
		}
	}
}

// Verify and read a webhook delivery in a net/http handler.
func Example_webhook() {
	secret := os.Getenv("RENDOBAR_WEBHOOK_SECRET")
	http.HandleFunc("/rendobar-webhook", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body) // the exact bytes the signature covers
		event, err := webhooks.Parse(body, r.Header, secret)
		if err != nil {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		payload, err := event.Payload()
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		switch data := payload.(type) {
		case *webhooks.JobCompletedData:
			if data.Output != nil && data.Output.File != nil { // a data job has no file
				fmt.Println(data.JobID, data.Output.File.URL)
			}
		case *webhooks.JobFailedData:
			fmt.Println(data.JobID, data.Error.Code)
		default:
			// An event this handler ignores, or one newer than this SDK.
		}
		w.WriteHeader(http.StatusNoContent)
	})
}
