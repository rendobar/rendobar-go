# Examples

Small programs that use the Go SDK the way your code would. Each folder is one `main` package. They
are also the SDK's regression gate: CI runs every one, and a change that breaks one cannot merge.

| Folder | What it does | Jobs (live) |
|---|---|---|
| `quickstart` | Probes a public video and prints its duration, resolution and codecs. | 1 ffprobe |
| `upload-process-download` | Uploads `fixtures/clip.mp4` with `Uploads.Create`, trims it with the upload as the input (`asset://`), streams the output to disk and checks its size and SHA-256, shares the output, then lists, reads and deletes the asset. | 1 ffmpeg |
| `batch-worker` | Submits jobs with idempotency keys, resubmits one, reuses a key with other params (409), triggers each error type, reads a job's logs and timings and the day's job counts, pages with `Jobs.All`, hits a wait timeout and a context deadline, cancels a job and reads a failed one. | 3 ffprobe, 2 ffmpeg |
| `webhook-receiver` | Creates a webhook endpoint and walks it through list, get, update, test, deliveries, retry and secret rotation, then deletes it. A `net/http` server verifies deliveries signed with that endpoint's secret and switches on typed events, with a default branch for events newer than the SDK. | none |
| `account-admin` | Reads the plan and limits, billing state, usage, transactions and invoices, API keys, the org and its settings (flips one and restores it), notifications, storage connections, job types, one type's schema and the compose effect catalogue, and checks a compose timeline. | none |

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
  Live mode cleans up after itself, also when a step fails: it deletes the uploaded asset, the
  share link and the webhook endpoint, and puts the org setting it changed back.

To run one example yourself, `go run ./quickstart -mode live` from this folder with
`RENDOBAR_API_KEY` set. `upload-process-download` reads `fixtures/clip.mp4` from here.
`RENDOBAR_API_URL` is optional and defaults to the production API.

The webhook receiver tests itself unless you pass `-serve :8080`, which serves real deliveries
signed with `RENDOBAR_WEBHOOK_SECRET`. In mock mode it also replays the shared webhook vectors
from `RENDOBAR_WEBHOOK_VECTORS` (by default `../webhooks/testdata/webhook-vectors.json`).

## What runs only in one mode

Each step that cannot run in a mode prints a `SKIP (<mode>: <why>)` line where it would be.

- Mock only: billing changes (checkouts, subscription, credit schedule, payment methods, the
  portal), creating, updating and revoking an API key (an API key cannot create keys), creating,
  renaming and deleting a workspace, member invitations and roles, and connecting storage (it
  needs a bucket's credentials). They never run live.
- Live only: the upload itself (the mock answers `POST /assets` without an upload URL), the
  download's size and SHA-256, a cancelled job ending cancelled and `Run` returning
  `JobFailedError` (every job the mock creates completes), and the checks that a new webhook
  endpoint is listed and a compose timeline is refused.
- `account-admin` reads storage connections only when the key holds the `storage:read` scope.
- Realtime progress has no example: the Go SDK has no realtime client yet.

## CI

- `ci.yml`, job `examples`: on every pull request, `staticcheck`, then `run.sh --mode mock`.
- `examples-live.yml`: `run.sh --mode live` against staging, by hand (`workflow_dispatch`), every
  Monday at 06:00 UTC, and on the release PR, so a release is checked live before it is tagged.
  It reads the `RENDOBAR_STAGING_API_KEY` and `RENDOBAR_STAGING_API_URL` secrets.
