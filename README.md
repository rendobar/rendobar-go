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

`Jobs.Run` submits the job and waits until it completes, fails or is cancelled, following the job's own time limit from your plan. A failed job returns `*rendobar.JobFailedError` with the job attached. `Jobs.Types` lists every live job type, and `Jobs.Schema` returns one type's params.

A retried submit never creates a second job: each `Jobs.Create` call sends one idempotency key on every retry. Pass `IdempotencyKey` yourself to make separate calls safe to repeat.

## Errors

```go
var apiErr *rendobar.Error
if errors.As(err, &apiErr) {
	log.Println(apiErr.Code, apiErr.StatusCode, apiErr.RequestID) // quote RequestID to support
}
if rendobar.IsNotFound(err) { ... }
```

429 and 5xx responses are retried twice, honouring `Retry-After`. Change it with `rendobar.WithMaxRetries`.

## Webhooks

```go
body, _ := io.ReadAll(r.Body) // the exact bytes the signature covers
if !webhooks.Verify(body, r.Header, secret) {
	w.WriteHeader(http.StatusUnauthorized)
	return
}
```

`Verify` rejects deliveries older than 5 minutes and accepts the previous secret during a rotation.

## Options

`WithAPIKey`, `WithBaseURL`, `WithTimeout`, `WithHTTPClient`, `WithMaxRetries`, `WithApp` (names your application in requests) and `WithDeprecationHandler` (called once per deprecated endpoint you use).

## Support policy

The two newest Go releases, as Go itself supports. Tested against the shared Rendobar conformance scenarios in [rendobar/openapi](https://github.com/rendobar/openapi).

## License

MIT. Rendobar is a product of Craftobar LLC.
