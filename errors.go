package rendobar

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/rendobar/rendobar-go/internal/api"
	"github.com/rendobar/rendobar-go/internal/api/core"
)

// Error is an error with a stable code: a refusal from the API (a 4xx or 5xx
// with Rendobar's error envelope), or a call the SDK refused before sending
// anything (StatusCode 0).
//
//	var apiErr *rendobar.Error
//	if errors.As(err, &apiErr) && apiErr.Code == "NOT_FOUND" { ... }
//
// The SDK's own codes are MISSING_API_KEY (no key set), INVALID_API_KEY (a key
// that does not start with rb_) and INVALID_ARGUMENT (such as a malformed job id).
type Error struct {
	// Code is the stable machine-readable code, such as NOT_FOUND or INSUFFICIENT_CREDITS.
	Code string
	// Message is the human-readable explanation.
	Message string
	// StatusCode is the HTTP status, or 0 when the SDK refused the call before sending it.
	StatusCode int
	// RequestID is the X-Request-Id of the failed request. Quote it to support.
	RequestID string
	// RetryAfter is how long the server asked the caller to wait (Retry-After), or 0.
	RetryAfter time.Duration
	// Problems lists what a VALIDATION_ERROR, PLAN_LIMIT or FEATURE_NOT_ENABLED
	// refusal found wrong, one entry per problem.
	Problems []Problem
	// Details is the error's raw detail as the API sent it, decoded from JSON.
	Details any
}

// Problem is one thing a refusal found wrong with a request.
type Problem struct {
	Path    string // where in the request, such as params.command, or ""
	Message string
	// UpgradeTo names the cheapest plan that allows the request, or "" when
	// no plan change helps or only an enterprise plan does.
	UpgradeTo string
}

func (e *Error) Error() string {
	switch {
	case e.StatusCode == 0:
		return fmt.Sprintf("rendobar: %s (%s)", e.Message, e.Code)
	case e.RequestID != "":
		return fmt.Sprintf("rendobar: %s (%d %s, request %s)", e.Message, e.StatusCode, e.Code, e.RequestID)
	}
	return fmt.Sprintf("rendobar: %s (%d %s)", e.Message, e.StatusCode, e.Code)
}

// IsNotFound reports whether err is a 404 from the API.
func IsNotFound(err error) bool { return statusIs(err, http.StatusNotFound) }

// IsRateLimited reports whether err is a 429 from the API.
func IsRateLimited(err error) bool { return statusIs(err, http.StatusTooManyRequests) }

// IsUnauthorized reports whether err is a 401 from the API: the API key is
// wrong or revoked. A client with no key fails before sending, with code MISSING_API_KEY.
func IsUnauthorized(err error) bool { return statusIs(err, http.StatusUnauthorized) }

// IsConflict reports whether err is a 409 from the API, such as an
// idempotency key reused with different params.
func IsConflict(err error) bool { return statusIs(err, http.StatusConflict) }

func statusIs(err error, status int) bool {
	var e *Error
	return errors.As(err, &e) && e.StatusCode == status
}

// local is an error the SDK raises before sending a request.
func local(code, format string, args ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, args...)}
}

var jobID = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// checkID refuses a job id the API could never have issued, so a value such
// as "" or "../x" is never sent as a path.
func checkID(id string) error {
	if !jobID.MatchString(id) || id == "." || id == ".." {
		return local("INVALID_ARGUMENT", "job id %q is not valid: use letters, digits, '.', '_' and '-'", id)
	}
	return nil
}

// JobFailedError is returned by Jobs.Run when the job ends failed or cancelled.
type JobFailedError struct {
	// Code is the job's error code, such as INPUT_UNREADABLE, or
	// JOB_CANCELLED for a cancelled job.
	Code string
	Job  *Job
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

func jobFailed(job *Job) *JobFailedError {
	code := ""
	switch {
	case job.Failed != nil:
		code = job.Failed.Error.Code
	case job.Cancelled != nil:
		code = "JOB_CANCELLED"
	}
	return &JobFailedError{Code: code, Job: job}
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

// SubmitError is returned by Jobs.Create and Jobs.Run when a submit fails in a
// way that may have left a job behind: ctx ended, the connection failed, the
// attempt timed out, or the API still answered 429 or 5xx after the last
// retry. Submit again with IdempotencyKey to get that job, or to create it if
// it does not exist. The API never creates a second job for one key.
//
// It unwraps to the cause, so errors.As(err, &apiErr) reads a 429 or 5xx and
// errors.Is(err, context.DeadlineExceeded) reports a deadline.
type SubmitError struct {
	// IdempotencyKey is the key the submit sent, yours or the one the SDK generated.
	IdempotencyKey string
	// Err is the cause.
	Err error
}

func (e *SubmitError) Error() string {
	return fmt.Sprintf("rendobar: submit with idempotency key %s failed, and the job may exist. Submit again with the same key: %v", e.IdempotencyKey, e.Err)
}

// Unwrap returns Err.
func (e *SubmitError) Unwrap() error { return e.Err }

// submitError wraps a failed submit in *SubmitError unless the API refused it
// outright, in which case no job exists and the refusal comes back as it is.
func submitError(ctx context.Context, err error, key string) error {
	if ctxErr := ctx.Err(); ctxErr != nil {
		if !errors.Is(err, ctxErr) {
			err = ctxErr
		}
		return &SubmitError{IdempotencyKey: key, Err: err}
	}
	err = fromGenerated(err)
	var apiErr *Error
	if errors.As(err, &apiErr) && apiErr.StatusCode != http.StatusTooManyRequests && apiErr.StatusCode < 500 {
		return err
	}
	return &SubmitError{IdempotencyKey: key, Err: err}
}

// ContextError is returned by Jobs.Wait and Jobs.Run when ctx is cancelled or
// its deadline passes after the job was submitted. The job keeps running on
// the API. Use JobID with Jobs.Get, Jobs.Wait or Jobs.Cancel.
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

// contextError returns a *ContextError when ctx has ended, else nil. err is
// what the call returned. It becomes Err when it already wraps the context's error.
func contextError(ctx context.Context, err error, jobID string, job *Job) *ContextError {
	ctxErr := ctx.Err()
	if ctxErr == nil {
		return nil
	}
	if !errors.Is(err, ctxErr) {
		err = ctxErr
	}
	return &ContextError{JobID: jobID, Job: job, Err: err}
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
	if ra, ok := retryAfter(apiErr.Header.Get("Retry-After"), time.Now()); ok {
		out.RetryAfter = ra
	}
	var b bodied
	if errors.As(err, &b) {
		if body := b.GetBody(); body != nil && body.Error != nil {
			out.Code = body.Error.Code
			out.Message = body.Error.Message
			out.Details = body.Error.Details
			out.Problems = problems(out.Code, out.Details)
		}
	}
	return out
}

// problems reads error.details as a list of {path, message, upgradeTo} on the
// codes that send one. Anything of another shape is skipped, never an error.
func problems(code string, details any) []Problem {
	if code != "VALIDATION_ERROR" && code != "PLAN_LIMIT" && code != "FEATURE_NOT_ENABLED" {
		return nil
	}
	items, ok := details.([]any)
	if !ok {
		return nil
	}
	var out []Problem
	for _, item := range items {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		message, _ := m["message"].(string)
		upgradeTo, _ := m["upgradeTo"].(string)
		out = append(out, Problem{Path: problemPath(m["path"]), Message: message, UpgradeTo: upgradeTo})
	}
	return out
}

// problemPath reads a path sent as "a.b" or as ["a", "b", 0].
func problemPath(v any) string {
	switch p := v.(type) {
	case string:
		return p
	case []any:
		parts := make([]string, 0, len(p))
		for _, seg := range p {
			parts = append(parts, fmt.Sprint(seg))
		}
		return strings.Join(parts, ".")
	}
	return ""
}
