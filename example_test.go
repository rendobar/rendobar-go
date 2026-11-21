package rendobar_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"

	"github.com/rendobar/rendobar-go"
	"github.com/rendobar/rendobar-go/webhooks"
)

// Run a job and read its output URL.
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
