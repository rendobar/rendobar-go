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
		Params: rendobar.FfmpegParams{
			Command: "ffmpeg -i https://cdn.rendobar.com/assets/examples/sample.mp4 -vf scale=1280:-2 out.mp4",
		},
	}, nil)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(job.Complete.Output.File.URL)
}
```

`Jobs.Run` submits the job and waits until it completes, fails or is cancelled. A failed or cancelled job returns `*rendobar.JobFailedError` with the job attached.

An ffmpeg job writes files, so `Output.File` is the file it wrote and `Output.Files` lists all of them. A data job such as ffprobe writes no file, and `Output.File` is nil. `JobComplete.Ffprobe` reads its result as the `FfprobeResult` the API's spec describes:

```go
job, err := rb.Jobs.Run(ctx, rendobar.CreateParams{
	Params: rendobar.FfprobeParams{Command: "https://cdn.rendobar.com/assets/examples/sample.mp4"},
}, nil)
if err != nil {
	log.Fatal(err)
}
probe, err := job.Complete.Ffprobe()
if err != nil {
	log.Fatal(err)
}
if s := probe.Summary; s != nil && s.Video != nil {
	fmt.Println(*s.Video.DurationSec, *s.Video.Video.Width, *s.Video.Video.Height)
}
```

`FfprobeSummary` holds one variant per kind of media (`Video`, `Audio`, `Image`, `Other`), matching `Kind`. A number ffprobe could not read is a nil pointer.

## Job params

Each job type has its own params struct, generated from the API's published schema: `FfmpegParams`, `FfprobeParams`, `CompressTargetParams`, `CaptionBurnParams`, `CaptionsAnimateParams`, `ImageUpscaleParams`, and one struct per model for image generation and editing, such as `ImageGenerateFlux2DevParams`. The struct sets the job type, so a misspelt param or a wrong value type fails to compile.

- A field with a fixed set of values has its own type and constants, such as `FfmpegComputeGPU`.
- A field that takes one of several shapes is an interface. Set it to one of the types its godoc lists, such as `ImageUpscaleSizingFactor{Factor: 2}` or `CompressTargetTargetPresetBalanced`.
- An optional number or boolean is a pointer. `rendobar.Ptr` makes one: `Timeout: rendobar.Ptr[int64](600)`.

```go
rendobar.CreateParams{
	Params: rendobar.ImageUpscaleParams{
		Model:  rendobar.ImageUpscaleModelSeedvr27b,
		Sizing: rendobar.ImageUpscaleSizingHeight{Height: 2160},
	},
	Inputs: map[string]string{"image": "https://example.com/photo.jpg"},
}
```

`Jobs.Create` checks the params against the schema before it sends anything, and refuses them with an `*rendobar.Error` whose code is `INVALID_ARGUMENT`, one `Problem` per mistake. `Params.Validate()` runs the same check on its own.

`Inputs` maps an input name to the URL of a file the API can fetch. `Jobs.Schema` lists the names a type takes. ffmpeg and ffprobe accept any name, and the command refers to the file by it.

For a job type this SDK predates, use `rendobar.CustomParams{Type: "new.type", Fields: map[string]any{...}}`. It is the only untyped path, and it refuses a type that already has a struct.

Compose jobs also go through `CustomParams`. The API keeps compose out of the typed submit schema on purpose, because its nested timeline objects have no stable names to generate from. `Jobs.Schema("compose")` describes its params.

`CreateParams.Callback` asks the API to POST the job's result to your URL when the job ends. It carries the same envelope as a webhook, so `webhooks.Parse` reads it.

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

`job.Info()` returns the rest of the shared fields, such as `Label`, `Cost` and `WebURL`. A status newer than this SDK leaves all six fields nil, and the methods still answer. `job.IsTerminal()` reports whether a job has finished. For a status newer than this SDK it reads the API's `terminal` flag, so `Jobs.Wait` stops on a final status it does not know.

`Jobs.List` returns one page. `ListParams.Limit` goes up to 100, and the API treats a larger value as 100. `Jobs.All` fetches every page for you, 100 jobs at a time. A negative `Limit` or `Offset` returns an `INVALID_ARGUMENT` error before any request, and so does a job ID that is not letters, digits, `.`, `_` and `-`.

`Jobs.Types` lists every live job type, and `Jobs.Schema` returns one type's params.

### Downloads

`Jobs.Download` streams a job's output file. Close the body when you are done:

```go
d, err := rb.Jobs.Download(ctx, jobID)
if err != nil {
	log.Fatal(err)
}
defer d.Body.Close()
_, err = io.Copy(file, d.Body)
```

`WithTimeout` bounds only the wait for the response headers. Your `ctx` bounds the body, so a large file takes as long as it needs. `Download.Size` is the length in bytes, or -1 when the API does not say. The file's SHA-256 is `OutputFile.SHA256` on the complete job.

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

### Idempotency

A retried submit never creates a second job. Each `Jobs.Create` call sends one idempotency key on every retry. Pass `IdempotencyKey` yourself, or one from `rendobar.GenerateIdempotencyKey`, to make separate calls safe to repeat. Repeating a key with the same params returns the job it created, with `Created.Replayed` set. Reusing a key with different params returns a 409 error with code `CONFLICT`.

## Errors

Check an error with `errors.As` and compare its `Code` with the `rendobar.Code...` constants:

```go
var apiErr *rendobar.Error
if errors.As(err, &apiErr) {
	switch apiErr.Code {
	case rendobar.CodeNotFound:
		// the job does not exist
	case rendobar.CodeRateLimited:
		time.Sleep(apiErr.RetryAfter)
	default:
		log.Println(apiErr.Code, apiErr.StatusCode, apiErr.RequestID) // quote RequestID to support
	}
}
```

The API may add codes. An unknown one arrives as its own string, so keep a default branch. When a refusal lists what was wrong, `Error.Problems` has each one, with a `Path`, a `Message` and, for a plan limit, the `UpgradeTo` plan. `Error.Details` is the raw detail as JSON.

Some calls fail before anything is sent, with `StatusCode` 0:

- `CodeMissingAPIKey`: the client has no key. Set `RENDOBAR_API_KEY` or pass `rendobar.WithAPIKey`. `Jobs.Types` and `Jobs.Schema` need no key.
- `CodeInvalidAPIKey`: the key does not start with `rb_`.
- `CodeInvalidArgument`: params that fail their schema, a malformed job ID or job type, or a negative `Limit` or `Offset`.

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

### Retries

429, 500, 502, 503 and 504 responses and connection errors are retried twice, with backoff from half a second and a `Retry-After` of up to 60 seconds honoured. Nothing else is retried. A 408, a 409, a 501 or any other 4xx fails at once, and so does an attempt that ran out its timeout. When `Retry-After` is longer than 60 seconds, or the wait would outlast your `ctx` deadline, the client returns the error at once with `Error.RetryAfter` set. `rendobar.WithMaxRetries(0)` sends each request once.

## Webhooks

Import `github.com/rendobar/rendobar-go/webhooks`. `webhooks.Parse` verifies a delivery against the raw body and reads it:

```go
body, _ := io.ReadAll(r.Body) // the exact bytes the signature covers
event, err := webhooks.Parse(body, r.Header, secret)
if err != nil {
	w.WriteHeader(http.StatusUnauthorized) // errors.Is(err, webhooks.ErrSignature) and the like name why
	return
}
payload, err := event.Payload()
if err != nil {
	w.WriteHeader(http.StatusBadRequest)
	return
}
switch data := payload.(type) {
case *webhooks.JobCompletedData:
	log.Println(data.JobID, data.Output.File.URL)
case *webhooks.JobFailedData:
	log.Println(data.JobID, data.Error.Code)
default:
	// an event this handler ignores, or one newer than this SDK
}
```

Every delivery is a JSON envelope:

```json
{"version": "1", "event": "job.completed", "deliveryId": "whd_...", "timestamp": 1763700000000, "orgId": "org_...", "data": {}}
```

`Event` carries `Version`, `Type`, `ID` (the delivery ID, the same on every retry of one delivery), `OrgID`, `SentAt` and the raw `Data`. `event.Payload()` reads `Data` as the type the spec defines for the event, such as `*JobCompletedData` or `*BalanceLowData`, one per event the `webhooks.Job...`, `webhooks.Balance...` and `webhooks.Test` constants name. An event newer than this SDK gives `*UnknownPayload` with its `Type` and raw `Data`. Timestamps in event data are `rendobar.UnixMillis`, and `.Time()` turns one into a `time.Time`.

Each delivery carries two headers:

- `X-Rendobar-Timestamp` is the send time in Unix seconds.
- `X-Rendobar-Signature` is `sha256=` followed by the hex HMAC-SHA256 of `<timestamp>.<raw body>`, keyed with your endpoint's signing secret. A signature without the `sha256=` prefix is refused.

While you rotate the secret, deliveries also carry `X-Rendobar-Signature-Previous`, signed with the old secret. Parse accepts either header, so your handler keeps working until you switch secrets. It refuses deliveries more than 5 minutes old or ahead (`ErrTimestamp`), a signature that does not match (`ErrSignature`), an empty or whitespace-only secret (`ErrNoSecret`) and a body that is not an event (`ErrPayload`). `webhooks.Verify` runs only the check and returns a bool. `ParseWith` and `VerifyWith` take your own tolerance and clock.

## Options

```go
rb := rendobar.New(
	rendobar.WithAPIKey(os.Getenv("MY_RENDOBAR_KEY")),
	rendobar.WithBaseURL("http://localhost:8787"),
	rendobar.WithTimeout(time.Minute),
)
```

- `WithAPIKey` sets the key. Without it the client reads `RENDOBAR_API_KEY`. With neither, calls that need a key fail with `CodeMissingAPIKey`.
- `WithBaseURL` sends every request to another API host, such as a test server or another Rendobar environment. The default is `https://api.rendobar.com`.
- `WithTimeout` bounds each attempt, 30 seconds by default, including reading the body. For `Jobs.Download` it bounds only the wait for the response headers. It wins over the `Timeout` of a `WithHTTPClient` client in either order.
- `WithHTTPClient` sets the HTTP client. The client you pass is copied and never changed.
- `WithMaxRetries` sets how many times a 429, 500, 502, 503, 504 or a connection error is retried. The default is 2.
- `WithApp` names your application in requests.
- `WithDeprecationHandler` is called once per deprecated endpoint you use. The default logs to stderr.

## Support policy

This module supports Go 1.23 and newer, as its `go.mod` states. CI tests Go 1.23 and the two newest Go releases. It runs the shared Rendobar conformance scenarios from [rendobar/openapi](https://github.com/rendobar/openapi).

## License

MIT. Rendobar is a product of Craftobar LLC.
