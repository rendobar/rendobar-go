package rendobar

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/rendobar/rendobar-go/internal/api"
	"github.com/rendobar/rendobar-go/internal/api/core"
)

// Error is a refusal from the API: a 4xx or 5xx with Rendobar's error envelope.
//
//	var apiErr *rendobar.Error
//	if errors.As(err, &apiErr) && apiErr.Code == "NOT_FOUND" { ... }
type Error struct {
	// Code is the stable machine-readable code, such as NOT_FOUND or INSUFFICIENT_CREDITS.
	Code string
	// Message is the human-readable explanation.
	Message string
	// StatusCode is the HTTP status.
	StatusCode int
	// RequestID is the X-Request-Id of the failed request. Quote it to support.
	RequestID string
	// RetryAfter is how long the server asked the caller to wait (Retry-After), or 0.
	RetryAfter time.Duration
	// Details is any structured detail the API sent, such as a refusal's problems.
	Details any
}

func (e *Error) Error() string {
	if e.RequestID != "" {
		return fmt.Sprintf("rendobar: %s (%d %s, request %s)", e.Message, e.StatusCode, e.Code, e.RequestID)
	}
	return fmt.Sprintf("rendobar: %s (%d %s)", e.Message, e.StatusCode, e.Code)
}

// IsNotFound reports whether err is a 404 from the API.
func IsNotFound(err error) bool { return statusIs(err, http.StatusNotFound) }

// IsRateLimited reports whether err is a 429 from the API.
func IsRateLimited(err error) bool { return statusIs(err, http.StatusTooManyRequests) }

// IsUnauthorized reports whether err is a 401 from the API: the API key is
// wrong or revoked. A client with no key at all fails before sending, with ErrNoAPIKey.
func IsUnauthorized(err error) bool { return statusIs(err, http.StatusUnauthorized) }

// IsConflict reports whether err is a 409 from the API, such as an
// idempotency key reused with different params.
func IsConflict(err error) bool { return statusIs(err, http.StatusConflict) }

// ErrNoAPIKey is returned, before anything is sent, by every call that needs
// an API key when the client has none.
var ErrNoAPIKey = errors.New("rendobar: no API key: set RENDOBAR_API_KEY or pass rendobar.WithAPIKey")

var errEmptyID = errors.New("rendobar: job id is empty")

func statusIs(err error, status int) bool {
	var e *Error
	return errors.As(err, &e) && e.StatusCode == status
}

// JobFailedError is returned by Jobs.Run when the job ends failed or cancelled.
type JobFailedError struct {
	Job *Job
}

func (e *JobFailedError) Error() string {
	if e == nil || e.Job == nil {
		return "rendobar: job failed"
	}
	if f := e.Job.Failed; f != nil {
		if f.Error.Code != "" {
			return fmt.Sprintf("rendobar: job %s failed: %s (%s)", f.ID, f.Error.Message, f.Error.Code)
		}
		return fmt.Sprintf("rendobar: job %s failed", f.ID)
	}
	return fmt.Sprintf("rendobar: job %s ended %s", e.Job.ID(), e.Job.Status())
}

// WaitTimeoutError is returned by Jobs.Wait when its own deadline (see
// WaitOptions.Timeout) passes first. The job may still finish.
type WaitTimeoutError struct {
	JobID      string
	LastStatus JobStatus
	Waited     time.Duration // how long Wait actually waited
	Job        *Job          // the last state Wait read
}

func (e *WaitTimeoutError) Error() string {
	return fmt.Sprintf("rendobar: job %s still %s after %s. It may still finish: call Jobs.Get later", e.JobID, e.LastStatus, roundWait(e.Waited))
}

// roundWait keeps a wait readable: 341ms, 2.4s, 5m0s.
func roundWait(d time.Duration) time.Duration {
	switch {
	case d < time.Second:
		return d.Round(time.Millisecond)
	case d < time.Minute:
		return d.Round(100 * time.Millisecond)
	}
	return d.Round(time.Second)
}

// ContextError is returned by Jobs.Create, Jobs.Wait and Jobs.Run when ctx is
// cancelled or its deadline passes. The job may exist and keep running on the API:
//
//   - When ctx ended after the submit, JobID is set. Use it with Jobs.Get,
//     Jobs.Wait or Jobs.Cancel.
//   - When ctx ended during the submit, JobID is "" and the API may still
//     have created the job. Submit again with IdempotencyKey to get that same
//     job instead of a second one.
//
// It unwraps to the context's error, so errors.Is(err, context.DeadlineExceeded)
// and errors.Is(err, context.Canceled) report the cause.
type ContextError struct {
	// JobID is the submitted job, or "" when ctx ended during the submit.
	JobID string
	// IdempotencyKey is the key the submit sent, yours or the one the SDK
	// generated. "" from Jobs.Wait, which submits nothing.
	IdempotencyKey string
	// Job is the last state Wait read, or nil when ctx ended before the first read.
	Job *Job
	// Err is the error that ended the call. It wraps the context's error.
	Err error
}

func (e *ContextError) Error() string {
	if e.JobID == "" {
		return fmt.Sprintf("rendobar: submit with idempotency key %s stopped: %v. The job may exist: submit again with the same key", e.IdempotencyKey, e.Err)
	}
	return fmt.Sprintf("rendobar: stopped waiting for job %s: %v", e.JobID, e.Err)
}

// contextError returns a *ContextError when ctx has ended, else nil. err is
// what the call returned. It becomes Err when it already wraps the context's error.
func contextError(ctx context.Context, err error, jobID, key string, job *Job) *ContextError {
	ctxErr := ctx.Err()
	if ctxErr == nil {
		return nil
	}
	if !errors.Is(err, ctxErr) {
		err = ctxErr
	}
	return &ContextError{JobID: jobID, IdempotencyKey: key, Job: job, Err: err}
}

// Unwrap returns Err.
func (e *ContextError) Unwrap() error { return e.Err }

type bodied interface{ GetBody() *api.ErrorResponse }

// fromGenerated turns the generated core's typed errors into *Error. Anything else passes through.
func fromGenerated(err error) error {
	if err == nil {
		return nil
	}
	var apiErr *core.APIError
	if !errors.As(err, &apiErr) {
		return err
	}
	out := &Error{
		Code:       "UNKNOWN_ERROR",
		Message:    fmt.Sprintf("request failed with status %d", apiErr.StatusCode),
		StatusCode: apiErr.StatusCode,
		RequestID:  apiErr.Header.Get(requestIDHeader),
	}
	if ra, ok := retryAfter(apiErr.Header.Get("Retry-After"), time.Now()); ok {
		out.RetryAfter = ra
	}
	var b bodied
	if errors.As(err, &b) {
		if body := b.GetBody(); body != nil && body.Error != nil {
			out.Code = body.Error.Code
			out.Message = body.Error.Message
			out.Details = body.Error.Details
		}
	}
	return out
}
