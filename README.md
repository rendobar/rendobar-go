# Rendobar Go SDK

The Go client for [Rendobar](https://rendobar.com). Run FFmpeg commands, render timeline videos, burn captions and generate images through one API, and get back a hosted URL.

```bash
go get github.com/rendobar/rendobar-go
```

Requires Go 1.23 or newer.

## Quickstart

Set `RENDOBAR_API_KEY`, save this as `main.go` and run `go run .`:

```go
package main

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/rendobar/rendobar-go"
)

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	rb := rendobar.New() // reads RENDOBAR_API_KEY

	job, err := rb.Jobs.Run(ctx, rendobar.CreateParams{
		Type:   "ffmpeg",
		Params: map[string]any{"command": "ffmpeg -i https://cdn.rendobar.com/assets/examples/sample.mp4 -vf scale=1280:-2 out.mp4"},
	}, nil)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(job.Complete.Output.File.URL)
}
```

`Jobs.Run` submits the job and waits until it completes, fails or is cancelled. A failed or cancelled job returns `*rendobar.JobFailedError` with the job attached.

An ffmpeg job writes files, so `Output.File` is the file it wrote and `Output.Files` lists all of them. A data job such as ffprobe writes no file. Its result is in `Output.Data` and `Output.File` is nil:

```go
job, err := rb.Jobs.Run(ctx, rendobar.CreateParams{
	Type:   "ffprobe",
	Params: map[string]any{"command": "https://cdn.rendobar.com/assets/examples/sample.mp4"},
}, nil)
if err != nil {
	log.Fatal(err)
}
fmt.Println(job.Complete.Output.Data)
```

## Jobs

A `rendobar.Job` has one field set per status: `Waiting`, `Dispatched`, `Running`, `Complete`, `Failed` or `Cancelled`. Its methods read what every status shares, so listing jobs needs no type switch:

```go
for job, err := range rb.Jobs.All(ctx, rendobar.ListParams{Status: rendobar.StatusFailed}) {
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(job.ID(), job.Type(), job.Status(), job.CreatedAt())
}
```

`job.Info()` returns the rest of the shared fields, such as `Label`, `Cost` and `WebURL`. The statuses are `StatusWaiting`, `StatusDispatched`, `StatusRunning`, `StatusComplete`, `StatusFailed` and `StatusCancelled`. A status newer than this SDK leaves all six fields nil, and the methods still answer.

`Jobs.List` returns one page. `ListParams.Limit` goes up to 100, and the API treats a larger value as 100. `Jobs.All` fetches every page for you, 100 jobs at a time. A negative `Limit` or `Offset` returns an `*rendobar.Error` with code `INVALID_ARGUMENT` before any request, and so does a job ID that is not letters, digits, `.`, `_` and `-`.

`Jobs.Types` lists every live job type, and `Jobs.Schema` returns one type's params.

### Waiting

`Jobs.Wait` returns a failed or cancelled job with a nil error. Only `Jobs.Run` turns those into `*rendobar.JobFailedError`, whose `Code` is the job's error code, or `JOB_CANCELLED`. After `Wait`, check the status before you read the output, because `job.Complete` is nil unless the job completed:

```go
job, err := rb.Jobs.Wait(ctx, id, nil)
if err != nil {
	log.Fatal(err)
}
if job.Status() != rendobar.StatusComplete {
	log.Fatalf("job %s ended %s", job.ID(), job.Status())
}
fmt.Println(job.Complete.Output.File.URL)
```

`Jobs.Wait` gives up after 5 minutes until the job reports its own time limit from your plan. It then waits that limit plus 2 minutes from the moment the job reported it. On a timeout it returns `*rendobar.WaitTimeoutError` with the job ID, the last status and the last job read. The job may still finish. `WaitOptions` sets your own `Timeout`, the poll interval and an `OnProgress` callback.

When your `ctx` ends after the submit, `Jobs.Run` and `Jobs.Wait` return `*rendobar.ContextError` with the job ID. See [Errors](#errors) for what to do with it.

### Idempotency

A retried submit never creates a second job. Each `Jobs.Create` call sends one idempotency key on every retry. Pass `IdempotencyKey` yourself, or one from `rendobar.GenerateIdempotencyKey`, to make separate calls safe to repeat. Repeating a key with the same params returns the job it created, with `Created.Replayed` set. Reusing a key with different params returns a 409 `*rendobar.Error` with code `CONFLICT`.

## Errors

```go
var apiErr *rendobar.Error
if errors.As(err, &apiErr) {
	log.Println(apiErr.Code, apiErr.StatusCode, apiErr.RequestID) // quote RequestID to support
}
if rendobar.IsNotFound(err) {
	// the job does not exist
}
```

`rendobar.IsNotFound`, `IsUnauthorized`, `IsConflict` and `IsRateLimited` test for a 404, 401, 409 and 429. A `VALIDATION_ERROR`, `PLAN_LIMIT` or `FEATURE_NOT_ENABLED` refusal lists what was wrong in `Error.Problems`, each with a `Path`, a `Message` and, for a plan limit, the `UpgradeTo` plan.

Some calls fail before anything is sent, with an `*rendobar.Error` whose `StatusCode` is 0:

- `MISSING_API_KEY`: the client has no key. Set `RENDOBAR_API_KEY` or pass `rendobar.WithAPIKey`. `Jobs.Types` and `Jobs.Schema` need no key.
- `INVALID_API_KEY`: the key does not start with `rb_`.
- `INVALID_ARGUMENT`: a malformed job ID, or a negative `Limit` or `Offset`.

### Failed submits

When a submit fails in a way that may have left a job behind, `Jobs.Create` and `Jobs.Run` return `*rendobar.SubmitError`. That covers your `ctx` ending, a connection error, an attempt that ran out its timeout, and a 429 or 5xx after the last retry. The API may have created the job and billed it. `IdempotencyKey` is the key the SDK sent, yours or the one it generated. Submit again with it to get that job instead of a second one. `SubmitError` unwraps to the cause, so `errors.As(err, &apiErr)` and `errors.Is(err, context.DeadlineExceeded)` still work. A plain refusal, such as a 400 or a 402, returns `*rendobar.Error` and created nothing.

```go
created, err := rb.Jobs.Create(ctx, params)
var cutOff *rendobar.SubmitError
if errors.As(err, &cutOff) {
	params.IdempotencyKey = cutOff.IdempotencyKey
	created, err = rb.Jobs.Create(ctx, params) // the same job, never a second one
}
```

### Cancelled waits

When your `ctx` is cancelled or its deadline passes after the submit, `Jobs.Run` and `Jobs.Wait` return `*rendobar.ContextError`. The job keeps running. `JobID` names it and `Job` is the last state read, so you can call `Jobs.Get`, `Jobs.Wait` or `Jobs.Cancel` later. `errors.Is(err, context.DeadlineExceeded)` and `errors.Is(err, context.Canceled)` work on it.

```go
var stopped *rendobar.ContextError
if errors.As(err, &stopped) {
	log.Println("job", stopped.JobID, "is still running")
}
```

### Retries

429, 500, 502, 503 and 504 responses and connection errors are retried twice, with backoff from half a second and a `Retry-After` of up to 60 seconds honoured. Nothing else is retried. A 408, a 409, a 501 or any other 4xx fails at once, and so does an attempt that ran out its timeout. When `Retry-After` is longer than 60 seconds, or the wait would outlast your `ctx` deadline, the client returns the error at once with `Error.RetryAfter` set, so `rendobar.IsRateLimited(err)` is true for a 429. `rendobar.WithMaxRetries(0)` sends each request once.

## Webhooks

Import `github.com/rendobar/rendobar-go/webhooks` and verify each delivery against the raw body:

```go
body, _ := io.ReadAll(r.Body) // the exact bytes the signature covers
if !webhooks.Verify(body, r.Header, secret) {
	w.WriteHeader(http.StatusUnauthorized)
	return
}
```

Each delivery carries two headers:

- `X-Rendobar-Timestamp` is the send time in Unix seconds.
- `X-Rendobar-Signature` is `sha256=` followed by the hex HMAC-SHA256 of `<timestamp>.<raw body>`, keyed with your endpoint's signing secret. `Verify` rejects a signature without the `sha256=` prefix.

While you rotate the secret, deliveries also carry `X-Rendobar-Signature-Previous`, signed with the old secret. `Verify` accepts either header, so your handler keeps working until you switch secrets. It rejects deliveries more than 5 minutes old or ahead, and returns false for an empty or whitespace-only secret and for a signature that is not hex. `webhooks.VerifyWith` takes your own tolerance and clock.

## Options

```go
rb := rendobar.New(
	rendobar.WithAPIKey(os.Getenv("MY_RENDOBAR_KEY")),
	rendobar.WithBaseURL("http://localhost:8787"),
	rendobar.WithTimeout(time.Minute),
)
```

- `WithAPIKey` sets the key. Without it the client reads `RENDOBAR_API_KEY`. With neither, calls that need a key fail with code `MISSING_API_KEY`.
- `WithBaseURL` sends every request to another API host, such as a test server or another Rendobar environment. The default is `https://api.rendobar.com`.
- `WithTimeout` bounds each attempt, 30 seconds by default, including reading the body, so it bounds a whole `Jobs.Download`. It wins over the `Timeout` of a `WithHTTPClient` client in either order.
- `WithHTTPClient` sets the HTTP client. The client you pass is copied and never changed.
- `WithMaxRetries` sets how many times a 429, 500, 502, 503, 504 or a connection error is retried. The default is 2.
- `WithApp` names your application in requests.
- `WithDeprecationHandler` is called once per deprecated endpoint you use. The default logs to stderr.

## Support policy

This module supports Go 1.23 and newer, as its `go.mod` states. CI tests Go 1.23 and the two newest Go releases. It runs the shared Rendobar conformance scenarios from [rendobar/openapi](https://github.com/rendobar/openapi).

## License

MIT. Rendobar is a product of Craftobar LLC.
