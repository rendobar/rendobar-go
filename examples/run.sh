#!/usr/bin/env bash
# Runs the examples: ./run.sh [--mode mock|live] [example ...]
#
# mock (default): RENDOBAR_API_URL is the conformance mock, http://127.0.0.1:4100
#   unless set, and must be a loopback address. The mock is reset before each example.
# live: RENDOBAR_API_KEY and RENDOBAR_API_URL must both be set. Jobs run and bill.
set -euo pipefail
cd "$(dirname "$0")"

mode=mock
examples=()
while [ $# -gt 0 ]; do
  case "$1" in
    --mode) mode="${2:-}"; shift 2 ;;
    --mode=*) mode="${1#--mode=}"; shift ;;
    -*) echo "usage: run.sh [--mode mock|live] [example ...]" >&2; exit 2 ;;
    *) examples+=("$1"); shift ;;
  esac
done
if [ ${#examples[@]} -eq 0 ]; then
  examples=(quickstart upload-process-download batch-worker webhook-receiver account-admin)
fi

case "$mode" in
  mock)
    export RENDOBAR_API_URL="${RENDOBAR_API_URL:-http://127.0.0.1:4100}"
    case "$RENDOBAR_API_URL" in
      http://127.0.0.1:*|http://localhost:*) ;;
      *) echo "mock mode needs a loopback RENDOBAR_API_URL, got $RENDOBAR_API_URL" >&2; exit 2 ;;
    esac
    export RENDOBAR_API_KEY=rb_examples_mock # never send a real key to the mock
    ;;
  live)
    : "${RENDOBAR_API_KEY:?live mode needs RENDOBAR_API_KEY}"
    : "${RENDOBAR_API_URL:?live mode needs RENDOBAR_API_URL}"
    ;;
  *) echo "--mode must be mock or live, got $mode" >&2; exit 2 ;;
esac

go vet ./...
GOARCH=386 go vet ./...

failed=()
for ex in "${examples[@]}"; do
  echo "== $ex ($mode)"
  if [ "$mode" = mock ]; then
    curl -sf -X POST "$RENDOBAR_API_URL/__reset" > /dev/null
  fi
  if ! go run "./$ex" -mode "$mode"; then
    failed+=("$ex")
  fi
done

if [ ${#failed[@]} -gt 0 ]; then
  echo "FAILED: ${failed[*]}" >&2
  exit 1
fi
echo "all ${#examples[@]} examples passed ($mode)"
