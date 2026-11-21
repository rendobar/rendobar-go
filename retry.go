package rendobar

import (
	"bytes"
	"context"
	"errors"
	"io"
	"math/rand/v2"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

const (
	retryBaseDelay = 500 * time.Millisecond
	retryMaxDelay  = 30 * time.Second
	// maxRetryAfter is the longest Retry-After the client waits out. A longer
	// one returns the error at once, with Error.RetryAfter set.
	maxRetryAfter = 60 * time.Second
)

// retrier sends each request and retries a 429, 500, 502, 503 or 504 or a
// connection error, honouring Retry-After up to 60 seconds. When the wait
// before a retry would outlast that cap or the context's deadline, it returns
// what the last attempt got instead of waiting. Nothing else is retried: a
// 408, a 409 or a 501 is the API's answer, not a transient failure. Each attempt goes through client.Do, so the
// client's Timeout bounds one attempt, not all of them.
//
// Retrying a submit is safe because Jobs.Create always sends an idempotency key.
type retrier struct {
	client     *http.Client
	maxRetries int
}

func (r *retrier) Do(req *http.Request) (*http.Response, error) {
	if req.Body != nil && req.Body != http.NoBody && req.GetBody == nil && r.maxRetries > 0 {
		body, err := io.ReadAll(req.Body)
		_ = req.Body.Close()
		if err != nil {
			return nil, err
		}
		req.Body = io.NopCloser(bytes.NewReader(body))
		req.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(body)), nil }
	}
	for attempt := 0; ; attempt++ {
		if attempt > 0 && req.GetBody != nil {
			body, err := req.GetBody()
			if err != nil {
				return nil, err
			}
			req.Body = body
		}
		res, err := r.client.Do(req)
		if attempt >= r.maxRetries || !retryable(req.Context(), res, err) {
			return res, err
		}
		delay := backoff(attempt)
		if res != nil {
			if d, ok := retryAfter(res.Header.Get("Retry-After"), time.Now()); ok {
				if d > maxRetryAfter {
					return res, nil // too long to wait: the caller gets the error with RetryAfter set
				}
				delay = d
			}
		}
		if deadline, ok := req.Context().Deadline(); ok && time.Until(deadline) < delay {
			// The retry would start after the caller's deadline. Return what this
			// attempt got, so a 429 still reads as rate limited, with its Retry-After.
			return res, err
		}
		if res != nil {
			_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 64<<10))
			_ = res.Body.Close()
		}
		timer := time.NewTimer(delay)
		select {
		case <-req.Context().Done():
			timer.Stop()
			return nil, req.Context().Err()
		case <-timer.C:
		}
	}
}

func retryable(ctx context.Context, res *http.Response, err error) bool {
	if err != nil {
		if ctx.Err() != nil {
			return false // the caller gave up
		}
		var urlErr *url.Error
		if errors.As(err, &urlErr) && urlErr.Timeout() {
			return false // the attempt used its whole timeout: do not multiply it
		}
		return true
	}
	switch res.StatusCode {
	case http.StatusTooManyRequests, http.StatusInternalServerError, http.StatusBadGateway,
		http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return true
	}
	return false // a 501 or any other status is the API's answer
}

// backoff is 0.5s, 1s, 2s... with 10% jitter either way, capped at 30s.
func backoff(attempt int) time.Duration {
	d := retryBaseDelay << min(attempt, 10)
	d = min(d, retryMaxDelay)
	return time.Duration(float64(d) * (0.9 + 0.2*rand.Float64()))
}

// retryAfter reads Retry-After as seconds or as an HTTP date.
func retryAfter(v string, now time.Time) (time.Duration, bool) {
	if v == "" {
		return 0, false
	}
	if secs, err := strconv.ParseFloat(v, 64); err == nil {
		if secs < 0 || secs != secs {
			return 0, false
		}
		secs = min(secs, 365*24*3600) // a year is past any sane wait, and keeps the Duration from overflowing
		return time.Duration(secs * float64(time.Second)), true
	}
	if at, err := http.ParseTime(v); err == nil {
		return max(at.Sub(now), 0), true
	}
	return 0, false
}
