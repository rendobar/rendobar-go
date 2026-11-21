package rendobar

import (
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

	err error // the generated core's error
}

// Unwrap returns the underlying error from the generated core.
func (e *Error) Unwrap() error { return e.err }

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

// ContextError is returned by Jobs.Wait and Jobs.Run when ctx is cancelled or
// its deadline passes after the job was submitted. The job keeps running on
// the API. Use JobID to call Jobs.Get, Jobs.Wait or Jobs.Cancel.
//
// It unwraps to the context's error, so errors.Is(err, context.DeadlineExceeded)
// and errors.Is(err, context.Canceled) report the cause.
type ContextError struct {
	JobID string
	// Job is the last state Wait read, or nil when ctx ended before the first read.
	Job *Job
	// Err is the error that ended the wait. It wraps the context's error.
	Err error
}

func (e *ContextError) Error() string {
	return fmt.Sprintf("rendobar: stopped waiting for job %s: %v", e.JobID, e.Err)
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
		err:        err,
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
