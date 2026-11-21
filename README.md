# Rendobar Go SDK

The Go client for [Rendobar](https://rendobar.com): run FFmpeg commands, render timeline videos, burn captions and generate images through one API, and get back a hosted URL.

```bash
go get github.com/rendobar/rendobar-go
```

## Quickstart

```go
rb := rendobar.New() // reads RENDOBAR_API_KEY

job, err := rb.Jobs.Run(ctx, rendobar.CreateParams{
	Type:   "ffmpeg",
	Params: map[string]any{"command": "ffmpeg -i https://cdn.rendobar.com/assets/examples/sample.mp4 -vf scale=1280:-2 out.mp4"},
}, nil)
if err != nil {
	return err
}
fmt.Println(job.Complete.Output.File.URL)
```

`Jobs.Run` submits the job and waits until it completes, fails or is cancelled. A failed or cancelled job returns `*rendobar.JobFailedError` with the job attached. `Jobs.Wait` gives up after 5 minutes until the job reports its own time limit from your plan, then waits that limit plus 2 minutes from the moment it was reported. On a timeout it returns `*rendobar.WaitTimeoutError`, and the job may still finish. `WaitOptions` sets your own `Timeout`, the poll interval and an `OnProgress` callback.

`Jobs.Types` lists every live job type, and `Jobs.Schema` returns one type's params as a `*rendobar.JobTypeSchema`. A `rendobar.Job` has one field set per status, typed `JobWaiting`, `JobDispatched`, `JobRunning`, `JobComplete`, `JobFailed` or `JobCancelled`.

A retried submit never creates a second job: each `Jobs.Create` call sends one idempotency key on every retry. Pass `IdempotencyKey` yourself to make separate calls safe to repeat.

## Errors

```go
var apiErr *rendobar.Error
if errors.As(err, &apiErr) {
	log.Println(apiErr.Code, apiErr.StatusCode, apiErr.RequestID) // quote RequestID to support
}
if rendobar.IsNotFound(err) { ... }
```

429 and 5xx responses and connection errors are retried twice, honouring `Retry-After`. Nothing else is retried: a 408, a 409 or any other 4xx fails at once, and so does an attempt that ran out its timeout. `rendobar.WithMaxRetries(0)` sends each request once. `Error.RetryAfter` is the server's `Retry-After` as a `time.Duration`, and `errors.Unwrap` on an `*Error` gives the underlying error.

## Webhooks

```go
body, _ := io.ReadAll(r.Body) // the exact bytes the signature covers
if !webhooks.Verify(body, r.Header, secret) {
	w.WriteHeader(http.StatusUnauthorized)
	return
}
```

`Verify` rejects deliveries older than 5 minutes and accepts the previous secret during a rotation. It returns false for an empty secret and for a signature that is not hex.

## Options

`WithAPIKey`, `WithBaseURL`, `WithTimeout`, `WithHTTPClient`, `WithMaxRetries`, `WithApp` (names your application in requests) and `WithDeprecationHandler` (called once per deprecated endpoint you use).

`WithTimeout` bounds each attempt, 30 seconds by default, including reading the body, so it bounds a whole `Jobs.Download`. It wins over the `Timeout` of a `WithHTTPClient` client in either order. The client you pass is copied and never changed.

## Support policy

The two newest Go releases, as Go itself supports. Tested against the shared Rendobar conformance scenarios in [rendobar/openapi](https://github.com/rendobar/openapi).

## License

MIT. Rendobar is a product of Craftobar LLC.
