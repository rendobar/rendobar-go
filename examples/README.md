# Examples

Small programs that use the Go SDK the way your code would. Each folder is one `main` package. They
are also the SDK's regression gate: CI runs every one, and a change that breaks one cannot merge.

| Folder | What it does | Jobs (live) |
|---|---|---|
| `quickstart` | Probes a public video and prints its duration, resolution and codecs. | 1 ffprobe |
| `upload-process-download` | Trims a clip with `Jobs.Create` and `Jobs.Wait`, streams the output to disk and checks its size and SHA-256. | 1 ffmpeg |
| `batch-worker` | Submits jobs with idempotency keys, resubmits one, reuses a key with other params (409), triggers each error type, pages with `Jobs.All`, hits a wait timeout and a context deadline, cancels a job and reads a failed one. | 3 ffprobe, 2 ffmpeg |
| `webhook-receiver` | A `net/http` server that verifies deliveries and switches on typed events, with a default branch for events newer than the SDK. | none |
| `account-admin` | Reads the plan, limits and features, lists job types and reads one type's schema. | none |

## Run them

The module in this folder builds against the SDK in the parent folder, through the `replace` in
`go.mod`. In your own module, `go get github.com/rendobar/rendobar-go` instead.

```bash
./run.sh --mode mock               # all of them, against the conformance mock
./run.sh --mode live quickstart    # one of them, against a real API
```

`run.sh` runs `go vet` (also for `GOARCH=386`), then each example, and exits non-zero if any fails.

- **mock** (the default) needs the conformance mock from
  [rendobar/openapi](https://github.com/rendobar/openapi) running on `RENDOBAR_API_URL`
  (`http://127.0.0.1:4100` by default), started as this repo's CI starts it. It costs nothing.
  A step the mock cannot answer prints `SKIP (mock: <why>)` and still runs in live mode.
- **live** needs `RENDOBAR_API_KEY` and `RENDOBAR_API_URL`. Jobs run and are billed: seven in all,
  each on a clip of a few seconds.

To run one example yourself, `go run ./quickstart -mode live` with `RENDOBAR_API_KEY` set.
`RENDOBAR_API_URL` is optional and defaults to the production API.

The webhook receiver tests itself unless you pass `-serve :8080`, which serves real deliveries
signed with `RENDOBAR_WEBHOOK_SECRET`. In mock mode it also replays the shared webhook vectors
from `RENDOBAR_WEBHOOK_VECTORS` (by default `../webhooks/testdata/webhook-vectors.json`).

## What the Go SDK does not cover

The Go SDK covers jobs, the account's capabilities and webhook verification. The examples skip
these steps, and each prints a `SKIP (sdk: ...)` line where the step would be:

- Uploads and assets, so `upload-process-download` reads a public URL and has no asset to list or delete.
- Job logs, in `batch-worker`.
- Webhook endpoint management (create, list, get, update, rotate the secret, send a test, list and
  retry deliveries, delete). `webhook-receiver` signs its own test deliveries with a local secret.
- API keys, billing, usage and org settings, in `account-admin`.
- Realtime progress. There is no `realtime-progress` example.

## CI

- `ci.yml`, job `examples`: on every pull request, `staticcheck`, then `run.sh --mode mock`.
- `examples-live.yml`: `run.sh --mode live` against staging, by hand (`workflow_dispatch`), every
  Monday at 06:00 UTC, and on the release PR, so a release is checked live before it is tagged.
  It reads the `RENDOBAR_STAGING_API_KEY` and `RENDOBAR_STAGING_API_URL` secrets.
