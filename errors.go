package rendobar

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/rendobar/rendobar-go/internal/api/core"
)

// ErrorCode is the stable machine-readable code of an *Error. The constants
// below are the codes this SDK knows. The API may add codes, and an unknown
// one arrives as its own string, so compare with == and keep a default branch.
type ErrorCode string

// Codes the SDK sets itself, before sending a request (StatusCode 0).
const (
	CodeMissingAPIKey   ErrorCode = "MISSING_API_KEY"  // no key: set RENDOBAR_API_KEY or pass WithAPIKey
	CodeInvalidAPIKey   ErrorCode = "INVALID_API_KEY"  // the key does not start with rb_
	CodeInvalidArgument ErrorCode = "INVALID_ARGUMENT" // params, an id or paging the SDK refused
	CodeUnknown         ErrorCode = "UNKNOWN_ERROR"    // the API answered without its error envelope
	CodeUploadFailed    ErrorCode = "UPLOAD_FAILED"    // a file part did not reach storage (Uploads.Create)
)

// Codes the API sends.
const (
	CodeUnauthorized         ErrorCode = "UNAUTHORIZED"
	CodeForbidden            ErrorCode = "FORBIDDEN"
	CodeInsufficientScope    ErrorCode = "INSUFFICIENT_SCOPE"
	CodeValidation           ErrorCode = "VALIDATION_ERROR"
	CodeInvalidJobType       ErrorCode = "INVALID_JOB_TYPE"
	CodeFeatureNotEnabled    ErrorCode = "FEATURE_NOT_ENABLED"
	CodeInsufficientCredits  ErrorCode = "INSUFFICIENT_CREDITS"
	CodePlanLimit            ErrorCode = "PLAN_LIMIT"
	CodeStorageQuotaExceeded ErrorCode = "STORAGE_QUOTA_EXCEEDED"
	CodeFileTooLarge         ErrorCode = "FILE_TOO_LARGE"
	CodeRateLimited          ErrorCode = "RATE_LIMITED"
	CodeQueueFull            ErrorCode = "QUEUE_FULL"
	CodeQueueExpired         ErrorCode = "QUEUE_EXPIRED"
	CodeNotFound             ErrorCode = "NOT_FOUND"
	CodeConflict             ErrorCode = "CONFLICT"
	CodeGone                 ErrorCode = "GONE"
	CodeOrgSuspended         ErrorCode = "ORG_SUSPENDED"
	CodeInputURLBlocked      ErrorCode = "INPUT_URL_BLOCKED"
	CodeInputNotMedia        ErrorCode = "INPUT_NOT_MEDIA"
	CodeInputUnsupported     ErrorCode = "INPUT_UNSUPPORTED"
	CodeInputFetchFailed     ErrorCode = "INPUT_FETCH_FAILED"
	CodeInputFetchStalled    ErrorCode = "INPUT_FETCH_STALLED"
	CodeProcessingFailed     ErrorCode = "PROCESSING_FAILED"
	CodeRunnerError          ErrorCode = "RUNNER_ERROR"
	CodeRunnerTimeout        ErrorCode = "RUNNER_TIMEOUT"
	CodeInternal             ErrorCode = "INTERNAL_ERROR"
	CodeNotImplemented       ErrorCode = "NOT_IMPLEMENTED"
	CodeUpstream             ErrorCode = "UPSTREAM_ERROR"
	CodeNotConfigured        ErrorCode = "NOT_CONFIGURED"
)

// Error is an error with a stable code: a refusal from the API (a 4xx or 5xx
// with Rendobar's error envelope), or a call the SDK refused before sending
// anything (StatusCode 0). Check it with errors.As and compare Code:
//
//	var apiErr *rendobar.Error
//	if errors.As(err, &apiErr) && apiErr.Code == rendobar.CodeNotFound { ... }
type Error struct {
	Code ErrorCode
	// Message is the human-readable explanation.
	Message string
	// StatusCode is the HTTP status, or 0 when the SDK refused the call before sending it.
	StatusCode int
	// RequestID is the X-Request-Id of the failed request. Quote it to support.
	RequestID string
	// RetryAfter is how long the server asked the caller to wait (Retry-After), or 0.
	RetryAfter time.Duration
	// Problems lists what the request got wrong, one entry each, when the
	// API (or Validate) sent a list of problems.
	Problems []Problem
	// Details is the error's detail as the API sent it, or nil.
	Details json.RawMessage
}

// Problem is one thing a refusal found wrong with a request.
type Problem struct {
	Path    string // where in the request, such as params.command, or ""
	Message string
	// UpgradeTo names the cheapest plan that allows the request, or "" when
	// no plan change helps or only an enterprise plan does.
	UpgradeTo PlanSlug
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

// local is an error the SDK raises before sending a request.
func local(code ErrorCode, format string, args ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, args...)}
}

var (
	jobID       = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)
	jobTypeName = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*$`)
)

// checkID refuses a job id the API could never have issued, so a value such
// as "" or "../x" is never sent as a path.
func checkID(id string) error {
	if !jobID.MatchString(id) || id == "." || id == ".." {
		return local(CodeInvalidArgument, "job id %q is not valid: use letters, digits, '.', '_' and '-'", id)
	}
	return nil
}

// JobFailedError is returned by Jobs.Run when the job ends failed or cancelled.
type JobFailedError struct {
	// Code is the job's error code, such as INPUT_UNREADABLE, or
	// JOB_CANCELLED for a cancelled job. Job codes are their own open set.
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

// fromGenerated turns the generated core's errors into *Error. Anything else passes through.
func fromGenerated(err error) error {
	if err == nil {
		return nil
	}
	var coreErr *core.APIError
	if !errors.As(err, &coreErr) {
		return err
	}
	var body []byte
	if raw := coreErr.Unwrap(); raw != nil {
		body = []byte(raw.Error()) // the core keeps the raw response body as this error's text
	}
	return apiError(coreErr.StatusCode, coreErr.Header, body)
}

// apiError reads Rendobar's error envelope, {"error": {code, message, details}}.
// A body of another shape still gives an *Error, with code UNKNOWN_ERROR.
func apiError(status int, header http.Header, body []byte) *Error {
	out := &Error{
		Code:       CodeUnknown,
		Message:    fmt.Sprintf("request failed with status %d", status),
		StatusCode: status,
		RequestID:  header.Get(requestIDHeader),
	}
	if ra, ok := retryAfter(header.Get("Retry-After"), time.Now()); ok {
		out.RetryAfter = ra
	}
	var env struct {
		Error *struct {
			Code    ErrorCode       `json:"code"`
			Message string          `json:"message"`
			Details json.RawMessage `json:"details"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &env) != nil || env.Error == nil {
		return out
	}
	if env.Error.Code != "" {
		out.Code = env.Error.Code
	}
	if env.Error.Message != "" {
		out.Message = env.Error.Message
	}
	if d := bytes.TrimSpace(env.Error.Details); len(d) > 0 && string(d) != "null" {
		out.Details = d
		out.Problems = problems(d)
	}
	return out
}

// problems reads details sent as a list of {path, message, upgradeTo}.
// Entries of another shape are skipped, never an error.
func problems(details json.RawMessage) []Problem {
	var items []json.RawMessage
	if json.Unmarshal(details, &items) != nil {
		return nil
	}
	var out []Problem
	for _, item := range items {
		var p struct {
			Path      json.RawMessage `json:"path"`
			Message   string          `json:"message"`
			UpgradeTo *string         `json:"upgradeTo"`
		}
		if json.Unmarshal(item, &p) != nil || p.Message == "" {
			continue
		}
		problem := Problem{Path: problemPath(p.Path), Message: p.Message}
		if p.UpgradeTo != nil {
			problem.UpgradeTo = PlanSlug(*p.UpgradeTo)
		}
		out = append(out, problem)
	}
	return out
}

// problemPath reads a path sent as "a.b" or as ["a", "b", 0].
func problemPath(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var parts []any
	if json.Unmarshal(raw, &parts) != nil {
		return ""
	}
	segs := make([]string, 0, len(parts))
	for _, p := range parts {
		segs = append(segs, fmt.Sprint(p))
	}
	return strings.Join(segs, ".")
}
