# Rendobar Go SDK

The Go client for [Rendobar](https://rendobar.com). Run FFmpeg commands, render timeline videos, burn captions and generate images through one API, and get back a hosted URL. It covers the whole API: jobs, uploads and assets, webhooks, storage, billing, API keys, workspaces, notifications and realtime events.

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
- A field that takes one of several shapes is an interface. Set it to one of the types its godoc lists, such as `ImageUpscaleSizingHeight` or `CompressTargetTargetPresetBalanced`.
- Every number and boolean is a pointer, required or not, so the Go type never changes when the API makes a field optional. `rendobar.Ptr` makes one: `Timeout: rendobar.Ptr[int64](600)`. Integers are always `int64`. Strings and enums are values, and `""` means unset.

```go
rendobar.CreateParams{
	Params: rendobar.ImageUpscaleParams{
		Model:  rendobar.ImageUpscaleModelSeedvr27b,
		Sizing: rendobar.ImageUpscaleSizingHeight{Height: rendobar.Ptr[int64](2160)},
	},
	Inputs: map[string]rendobar.Input{"source": rendobar.URL("https://example.com/photo.jpg")},
}
```

`Jobs.Create` checks the params against the schema before it sends anything, and refuses them with an `*rendobar.Error` whose code is `INVALID_ARGUMENT`, one `Problem` per mistake. `Params.Validate()` runs the same check on its own.

`Inputs` maps an input name to its value. `Jobs.Schema` lists the names a type takes. ffmpeg and ffprobe accept any file name, and the command refers to the file by it. A value is one of:

- `rendobar.URL("https://...")`: one file the API fetches. An uploaded asset's content URL works too.
- `rendobar.URLs{...}`: a list of files, such as image.edit's `"images"`, one to four pictures to edit.
- `rendobar.Content("...")`: a small text file sent inline, up to 64 KiB, such as an ffmpeg concat list.
- `rendobar.FromJob("job_...")`: a completed job's output, to chain ffmpeg jobs.
- `rendobar.Fonts{{Family: "Display", Src: "https://..."}}`: font files for text, under names the params use.

```go
rendobar.CreateParams{
	Params: rendobar.ImageEditQwenImageEdit2511Params{Prompt: "make the sky purple"},
	Inputs: map[string]rendobar.Input{"images": rendobar.URLs{"https://example.com/photo.jpg"}},
}
```

`rendobar.CustomParams{Type: ..., Fields: map[string]any{...}}` is the one unchecked path. It takes any job type: one this SDK predates, or a typed one when you need a param or value the API added after this release. The SDK checks only the type name, and the API checks the rest. `rendobar.JobTypeFfmpeg` and the other `JobType` constants name the types this SDK has structs for.

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

`Jobs.Logs` returns a job's log entries, `Jobs.Timings` how long each stage took, and `Jobs.Stats` the workspace's job counts, success rate and spend over a `StatsWindow`. `Jobs.RetryDeliveries` sends a completed job's output again to the destinations that missed it. `Jobs.CreateShare`, `Jobs.GetShare` and `Jobs.DeleteShare` manage a public link to the output.

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

`WithTimeout` bounds only the wait for the response headers. Your `ctx` bounds the body, so a large file takes as long as it needs. `Download.Size` is the length in bytes, or -1 when the API does not say. `Download.Filename` is the suggested name without any directory part, so it is safe to join to your own directory. The file's SHA-256 is `OutputFile.SHA256` on the complete job.

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

## Uploads and assets

`Uploads.Create` sends a local file and returns the ready `*rendobar.Asset`. It takes an `io.ReaderAt` and the size, so an `*os.File` works. A large file goes up in parts, five at a time by default, and each part is retried up to 3 times:

```go
f, err := os.Open("clip.mp4")
if err != nil {
	log.Fatal(err)
}
defer f.Close()
info, err := f.Stat()
if err != nil {
	log.Fatal(err)
}
asset, err := rb.Uploads.Create(ctx, f, info.Size(), rendobar.UploadParams{
	Filename:   "clip.mp4",
	OnProgress: func(p rendobar.UploadProgress) { fmt.Println(p.Loaded, "of", p.Total) },
})
if err != nil {
	log.Fatal(err)
}
job, err := rb.Jobs.Run(ctx, rendobar.CreateParams{
	Params: rendobar.FfmpegParams{Command: "ffmpeg -i clip.mp4 -t 1 out.mp4"},
	Inputs: map[string]rendobar.Input{"clip.mp4": rendobar.URL("asset://" + asset.ID)},
}, nil)
if err != nil {
	log.Fatal(err)
}
fmt.Println(job.Complete.Output.File.URL)
```

An upload expires after 24 hours unless `UploadParams.Persist` is set. `ctx` bounds the whole upload. `WithTimeout` does not apply to the file transfer: each attempt gets a budget that grows with the file size. `Assets.List`, `Assets.Get`, `Assets.Delete` and `Assets.Templates` manage what the workspace holds, and `Assets.Download` returns a short-lived download URL.

## The rest of the API

Every resource of the API has its own field on the client, under the names `@rendobar/sdk` uses:

| Field | Calls |
|---|---|
| `Billing` | `State`, `Usage`, `UsageByClient`, `Transactions`, `History`, `CheckoutCredits`, `CheckoutPlan`, `CancelSubscription`, `ReactivateSubscription`, `CancelCreditSchedule`, `PaymentMethods`, `DeletePaymentMethod`, `SetDefaultPaymentMethod`, `PortalURL` |
| `Webhooks` | `Create`, `List`, `Get`, `Update`, `Delete`, `RotateSecret`, `Test`, `ListDeliveries`, `RetryDelivery` |
| `Storage` | `Create`, `List`, `Get`, `Update`, `Delete`, `Test`, `Stats`, `ListObjects`, `PrepareAWS`, `ListAccountBuckets`, `LookUpS3Bucket`, `StartSupabase`, `ListSupabaseProjects`, `ListSupabaseBuckets`, `FinishSupabase` |
| `APIKeys` | `Create`, `List`, `Update`, `Revoke` |
| `Orgs` | `List`, `Current`, `Create`, `Update`, `Delete`, `GetSettings`, `UpdateSettings`; `Orgs.Members`: `Invite`, `ChangeRole`, `Remove`; `Orgs.Invitations`: `Revoke` |
| `Account` | `Capabilities` |
| `Compose` | `Effects`, `EngineEffects`, `Validate` |
| `Notifications` | `List`, `MarkRead` |

Every call takes a `ctx`. A call that sends a body takes a params struct, such as `CreateWebhookParams`, generated from the API's spec with the same rules as job params. The SDK checks it before sending and refuses a missing field or a value outside a fixed list with an `INVALID_ARGUMENT` `*Error`, one `Problem` each. In an update, a field you leave zero stays as it is:

```go
endpoint, err := rb.Webhooks.Create(ctx, rendobar.CreateWebhookParams{
	Name:             "orders",
	URL:              "https://example.com/rendobar-webhook",
	SubscribedEvents: []rendobar.WebhookEvent{webhooks.JobCompleted, webhooks.JobFailed},
})
if err != nil {
	log.Fatal(err)
}
fmt.Println(endpoint.SigningSecret) // shown in full here and by RotateSecret only

_, err = rb.Webhooks.Update(ctx, endpoint.ID, rendobar.UpdateWebhookParams{Active: rendobar.Ptr(false)})
```

`Storage.Create` takes one of `CreateStorageS3Params`, `CreateStorageR2Params`, `CreateStorageS3CompatibleParams` or `CreateStorageSupabaseParams`, and its credentials are one of `StorageKeys`, `StorageAWSRole`, `StorageCloudflareToken` or `StorageSupabaseSession`, whichever the provider takes.

Responses are their own types, such as `*rendobar.BillingState` or `*rendobar.WebhookDelivery`, also generated from the spec. A field with a fixed set of values has its own type and constants, such as `WebhookDeliveryStatusFailed`. The API may add values, so keep a default branch. Integers are `int64` and numbers `float64`, as the spec types them. A field's doc gives its unit where the spec states one, such as nanodollars or dollars. Timestamps are `rendobar.UnixMillis`. A list comes back as a page with its total, such as `*rendobar.AssetPage` with `Assets` and `Meta`.

Some calls need more than an API key. An API key cannot create API keys, and a key holds only the scopes it was created with, so a call outside them returns a 403 `*Error` with code `INSUFFICIENT_SCOPE`.

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
- `CodeInvalidArgument`: params or a request body that fail their schema, a malformed ID or job type, or a negative `Limit`, `Offset` or `Page`.

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

A 429 is retried twice on every call, because the API did nothing with the request. A 500, 502, 503 or 504 and a connection error are retried twice on a read (GET) and on a job submit, which carries its idempotency key. A create, update or delete that fails that way returns the error at once, because the API may have acted on it: read the state back before you send it again. Retries back off from half a second and honour a `Retry-After` of up to 60 seconds. Nothing else is retried. A 408, a 409, a 501 or any other 4xx fails at once, and so does an attempt that ran out its timeout. When `Retry-After` is longer than 60 seconds, or the wait would outlast your `ctx` deadline, the client returns the error at once with `Error.RetryAfter` set. `rendobar.WithMaxRetries(0)` sends each request once.

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
	if data.Output != nil && data.Output.File != nil { // a data job, such as ffprobe, has no file
		log.Println(data.JobID, data.Output.File.URL)
	}
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

An envelope whose `version` is not `"1"` gives `*UnknownPayload`, because its data may have another shape. `Event` carries `Version`, `Type`, `ID` (the delivery ID, the same on every retry of one delivery), `OrgID`, `SentAt` and the raw `Data`. `Type` is a `rendobar.WebhookEvent`, the type `CreateWebhookParams.SubscribedEvents` takes. `event.Payload()` reads `Data` as the type the spec defines for the event, such as `*JobCompletedData` or `*BalanceLowData`, one per event the `webhooks.Job...`, `webhooks.Balance...` and `webhooks.Test` constants name. An event newer than this SDK gives `*UnknownPayload` with its `Type` and raw `Data`. Timestamps in event data are `rendobar.UnixMillis`, and `.Time()` turns one into a `time.Time`.

Each delivery carries two headers:

- `X-Rendobar-Timestamp` is the send time in Unix seconds.
- `X-Rendobar-Signature` is `sha256=` followed by the hex HMAC-SHA256 of `<timestamp>.<raw body>`, keyed with your endpoint's signing secret. A signature without the `sha256=` prefix is refused.

While you rotate the secret, deliveries also carry `X-Rendobar-Signature-Previous`, signed with the old secret. Parse accepts either header, so your handler keeps working until you switch secrets. It refuses deliveries more than 5 minutes old or ahead (`ErrTimestamp`), a signature that does not match (`ErrSignature`), an empty or whitespace-only secret (`ErrNoSecret`) and a body that is not an event (`ErrPayload`). `webhooks.Verify` runs only the check and returns a bool. `ParseWith` and `VerifyWith` take your own tolerance and clock.

## Realtime

Import `github.com/rendobar/rendobar-go/realtime` to stream events as they happen. It is its own package, so programs that do not import it do not build in its WebSocket library. `Client.Job` follows one job and returns after its final status:

```go
rt := realtime.New() // reads RENDOBAR_API_KEY, like rendobar.New
for ev, err := range rt.Job(ctx, created.ID) {
	if err != nil {
		log.Fatal(err)
	}
	switch e := ev.(type) {
	case *realtime.JobProgress:
		if e.Progress != nil {
			fmt.Printf("%s %.0f%%\n", e.Phase, *e.Progress*100)
		}
	case *realtime.JobLog:
		fmt.Println(e.Level, e.Message)
	case *realtime.JobStatusChanged:
		fmt.Println("status", e.Status)
	default:
		// another event, or *realtime.Unknown for one newer than this SDK
	}
}
```

`Client.Events` streams every event of the workspace, such as `*realtime.BalanceUpdated` and `*realtime.Notification`, until your `ctx` ends. It yields `*realtime.Connected` on each connection, the events the workspace kept, `*realtime.ReplayDone`, then live events. The stream reconnects with backoff and resumes after the last event it yielded, so you never see one twice. To resume in a later process, store `ev.EventID()` and pass it as `EventsParams.LastEventID`. `*realtime.Resync` says events were lost: read your state again. A refusal no reconnect fixes, such as a revoked key or one without the `jobs:read` scope, ends the stream with a `*rendobar.Error`. The key travels in the `Authorization` header, never in the URL.

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
- `WithApp` names your application in requests, 1 to 32 letters, digits or `. _ + -`. Another name makes every call return an `INVALID_ARGUMENT` error.
- `WithDeprecationHandler` is called once per deprecated endpoint you use. The default logs to stderr.

## Examples

[`examples/`](examples) has small programs that use the SDK the way your code would: a quickstart, an
upload, trim and download, a batch worker, a webhook receiver that manages its own endpoint, and an
account admin.

## Support policy

This module supports Go 1.23 and newer, as its `go.mod` states. CI tests Go 1.23 and the two newest Go releases. It runs the shared Rendobar conformance scenarios from [rendobar/openapi](https://github.com/rendobar/openapi).

## License

MIT. Rendobar is a product of Craftobar LLC.
