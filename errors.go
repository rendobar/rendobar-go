package rendobar

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"

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
	// RetryAfter is the server's Retry-After in seconds, or 0.
	RetryAfter float64
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

func statusIs(err error, status int) bool {
	var e *Error
	return errors.As(err, &e) && e.StatusCode == status
}

// JobFailedError is returned by Jobs.Run when the job ends failed or cancelled.
type JobFailedError struct {
	Job *Job
}

func (e *JobFailedError) Error() string {
	if e.Job.Failed != nil {
		return fmt.Sprintf("rendobar: job %s failed: %s (%s)", e.Job.Failed.ID, e.Job.Failed.Error.Message, e.Job.Failed.Error.Code)
	}
	return fmt.Sprintf("rendobar: job ended %s", e.Job.Status)
}

// WaitTimeoutError is returned by Jobs.Wait when the deadline passes first. The job may still finish.
type WaitTimeoutError struct {
	JobID      string
	LastStatus string
	Waited     float64 // seconds
}

func (e *WaitTimeoutError) Error() string {
	return fmt.Sprintf("rendobar: job %s still %s after %.0fs. It may still finish: call Jobs.Get later", e.JobID, e.LastStatus, e.Waited)
}

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
	if ra, perr := strconv.ParseFloat(apiErr.Header.Get("Retry-After"), 64); perr == nil {
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
