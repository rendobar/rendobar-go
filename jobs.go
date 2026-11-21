package rendobar

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"iter"
	"time"

	"github.com/rendobar/rendobar-go/internal/api"
)

// Variables, not constants, so the tests can shorten them.
var (
	defaultWaitTimeout = 5 * time.Minute // from the start of Wait, until the job reports its own budget
	waitGrace          = 2 * time.Minute // headroom past that budget for upload and callback
)

// Jobs submits jobs, follows them to the end, and reads their output.
type Jobs struct{ client *Client }

// CreateParams is a job submit.
type CreateParams struct {
	// Type is the job type, such as "ffmpeg" or "ffprobe". Jobs.Types lists them.
	Type string
	// Params is the job type's params: a map, or any struct that encodes to
	// the JSON the type's schema describes (see Jobs.Schema).
	Params any
	// Inputs maps an input name to a file URL, such as
	// {"source": "https://example.com/in.mp4"}. Jobs.Schema lists the names a
	// type takes. ffmpeg and ffprobe accept any name, and the command refers
	// to the file by it. A command that names its input URLs needs no Inputs.
	Inputs map[string]any
	// IdempotencyKey makes the submit safe to repeat. With none, the SDK
	// generates one per Create call, so its own retries cannot create a second job.
	// Reusing a key with different Type, Params or Inputs is refused with a
	// 409 CONFLICT *Error (see IsConflict).
	IdempotencyKey string
	// Label is a short name for the job, up to 200 characters, such as "Trim
	// intro for episode 12". The dashboard shows it, and JobInfo.Label returns it.
	Label string
	// Destinations are your storage connections to deliver the output to once
	// the job completes, as "storage://<id>", "storage://<id>/<folder>" or
	// "storage://<id>/<path template>". A path template may use {job_id},
	// {ext}, {source_name} and {date}. JobInfo.Deliveries reports each one.
	Destinations []string
	// ForceAsync makes the submit return as soon as the job is queued, even
	// for a job type the API could finish within the submit request.
	ForceAsync bool
}

// Created is what a submit returns: the job id and its status at that moment.
type Created struct {
	ID     string    `json:"id"`
	Status JobStatus `json:"status"`
}

// GenerateIdempotencyKey returns "sdk_" plus 32 hex characters: unique per call, not secret.
func GenerateIdempotencyKey() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(fmt.Sprintf("rendobar: crypto/rand failed: %v", err))
	}
	return "sdk_" + hex.EncodeToString(b[:])
}

func toMap(v any) (map[string]any, error) {
	if v == nil {
		return map[string]any{}, nil
	}
	if m, ok := v.(map[string]any); ok {
		return m, nil
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("rendobar: params do not encode to JSON: %w", err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("rendobar: params must encode to a JSON object: %w", err)
	}
	return m, nil
}

// Create submits a job and returns at once. Safe to retry: one key rides every retry of this call.
//
// When ctx ends before the API answers, the job may still have been created.
// Create then returns a *ContextError whose IdempotencyKey is the key it sent.
// Submit again with that key to get the same job.
func (j *Jobs) Create(ctx context.Context, p CreateParams) (*Created, error) {
	if err := j.client.needKey(); err != nil {
		return nil, err
	}
	params, err := toMap(p.Params)
	if err != nil {
		return nil, err
	}
	key := p.IdempotencyKey
	if key == "" {
		key = GenerateIdempotencyKey()
	}
	body := &api.SubmitAnyJob{Type: p.Type, Params: params, Inputs: p.Inputs, IdempotencyKey: &key, Destinations: p.Destinations}
	if p.Label != "" {
		body.Label = &p.Label
	}
	if p.ForceAsync {
		body.ForceAsync = &p.ForceAsync
	}
	res, err := j.client.core.Jobs.SubmitJob(ctx, &api.SubmitJobRequest{SubmitAnyJob: body})
	if err != nil {
		if ce := contextError(ctx, err, "", key, nil); ce != nil {
			return nil, ce
		}
		return nil, fromGenerated(err)
	}
	// The data is one of several documented shapes. Each carries id and status.
	raw, err := json.Marshal(res.Data)
	if err != nil {
		return nil, fmt.Errorf("rendobar: unreadable submit response: %w", err)
	}
	var out Created
	if err := json.Unmarshal(raw, &out); err != nil || out.ID == "" {
		return nil, fmt.Errorf("rendobar: submit response has no job id")
	}
	return &out, nil
}

// Get returns the job now.
func (j *Jobs) Get(ctx context.Context, id string) (*Job, error) {
	if err := j.client.needKey(); err != nil {
		return nil, err
	}
	if id == "" {
		return nil, errEmptyID
	}
	res, err := j.client.core.Jobs.GetJob(ctx, &api.GetJobRequest{ID: id})
	if err != nil {
		return nil, fromGenerated(err)
	}
	return fromAPIJob(res.Data)
}

// ListParams filters Jobs.List and Jobs.All. The zero value lists every job.
type ListParams struct {
	// Status keeps jobs in one status, such as StatusFailed. "" keeps all.
	Status JobStatus
	// Type keeps jobs of one type, such as "ffmpeg". "" keeps all.
	Type string
	// Limit is the page size, up to 100. The API treats a larger value as 100.
	// 0 uses the API's default of 50 for List, and 100 for All. A negative
	// Limit is an error.
	Limit int
	// Offset skips that many matching jobs. A negative Offset is an error.
	Offset int
}

// Page is one page of jobs, newest first, and the total that matched.
type Page struct {
	Jobs  []*Job
	Total int
}

// List returns one page of jobs.
func (j *Jobs) List(ctx context.Context, p ListParams) (*Page, error) {
	if err := j.client.needKey(); err != nil {
		return nil, err
	}
	if p.Limit < 0 || p.Offset < 0 {
		return nil, fmt.Errorf("rendobar: ListParams Limit and Offset must not be negative, got %d and %d", p.Limit, p.Offset)
	}
	req := &api.ListJobsRequest{}
	if p.Status != "" {
		s := api.ListJobsRequestStatus(p.Status)
		req.Status = &s
	}
	if p.Type != "" {
		req.Type = &p.Type
	}
	if p.Limit > 0 {
		req.Limit = &p.Limit
	}
	if p.Offset > 0 {
		req.Offset = &p.Offset
	}
	res, err := j.client.core.Jobs.ListJobs(ctx, req)
	if err != nil {
		return nil, fromGenerated(err)
	}
	page := &Page{Jobs: make([]*Job, 0, len(res.Data))}
	for _, in := range res.Data {
		job, err := fromAPIJob(in)
		if err != nil {
			return nil, err
		}
		page.Jobs = append(page.Jobs, job)
	}
	if res.Meta != nil {
		page.Total = res.Meta.Total
	}
	return page, nil
}

// All yields every matching job, fetching pages as you range over it.
//
//	for job, err := range rb.Jobs.All(ctx, rendobar.ListParams{Status: rendobar.StatusFailed}) { ... }
func (j *Jobs) All(ctx context.Context, p ListParams) iter.Seq2[*Job, error] {
	return func(yield func(*Job, error) bool) {
		q := p // a copy per range, so ranging the same sequence twice yields everything twice
		if q.Limit <= 0 {
			q.Limit = 100
		}
		for {
			page, err := j.List(ctx, q)
			if err != nil {
				yield(nil, err)
				return
			}
			for _, job := range page.Jobs {
				if !yield(job, nil) {
					return
				}
			}
			q.Offset += len(page.Jobs)
			if len(page.Jobs) == 0 || q.Offset >= page.Total {
				return
			}
		}
	}
}

// Cancel cancels a job that has not finished.
func (j *Jobs) Cancel(ctx context.Context, id string) error {
	if err := j.client.needKey(); err != nil {
		return err
	}
	if id == "" {
		return errEmptyID
	}
	_, err := j.client.core.Jobs.CancelJob(ctx, &api.CancelJobRequest{ID: id})
	return fromGenerated(err)
}

// Download returns the output file of a completed job, held in memory.
// WithTimeout bounds the whole download, not only its first byte.
func (j *Jobs) Download(ctx context.Context, id string) ([]byte, error) {
	if err := j.client.needKey(); err != nil {
		return nil, err
	}
	if id == "" {
		return nil, errEmptyID
	}
	r, err := j.client.core.Jobs.DownloadJobOutput(ctx, &api.DownloadJobOutputRequest{ID: id})
	if err != nil {
		return nil, fromGenerated(err)
	}
	if buf, ok := r.(*bytes.Buffer); ok {
		return buf.Bytes(), nil // the core already buffered it: hand those bytes over, no second copy
	}
	return io.ReadAll(r)
}

// Types returns every live job type. Public: needs no API key.
func (j *Jobs) Types(ctx context.Context) ([]JobType, error) {
	res, err := j.client.core.Jobs.ListJobTypes(ctx)
	if err != nil {
		return nil, fromGenerated(err)
	}
	types, err := convert[[]JobType](res.Data)
	if err != nil {
		return nil, err
	}
	return *types, nil
}

// Schema returns one job type's params: its fields, inputs and full JSON Schema. Public: needs no API key.
func (j *Jobs) Schema(ctx context.Context, jobType string) (*JobTypeSchema, error) {
	res, err := j.client.core.Jobs.GetJobTypeSchema(ctx, &api.GetJobTypeSchemaRequest{Type: jobType})
	if err != nil {
		return nil, fromGenerated(err)
	}
	return convert[JobTypeSchema](res.Data)
}

// WaitOptions tune Jobs.Wait and Jobs.Run. The zero value is fine.
type WaitOptions struct {
	// Timeout overrides the deadline. By default Wait gives up 5 minutes after it
	// starts, a job stuck in the queue included. When the job first reports its own
	// time limit, the deadline moves out to that limit plus 2 minutes, counted from
	// that poll, if that is later.
	Timeout     time.Duration
	Interval    time.Duration // first poll gap, default 2s
	MaxInterval time.Duration // longest poll gap, default 10s
	OnProgress  func(*Job)
}

// Wait polls until the job completes, fails or is cancelled, and returns it.
//
// A failed or cancelled job is not an error here. Wait returns it with a nil
// error, and only a complete job has job.Complete set. Check job.Status()
// before reading job.Complete.Output, or use Jobs.Run, which returns
// *JobFailedError for a failed or cancelled job.
//
// When ctx is cancelled or its deadline passes, Wait returns a *ContextError
// with the job id and the last job it read. errors.Is(err,
// context.DeadlineExceeded) and errors.Is(err, context.Canceled) still work.
func (j *Jobs) Wait(ctx context.Context, id string, o *WaitOptions) (*Job, error) {
	if o == nil {
		o = &WaitOptions{}
	}
	interval, maxInterval := o.Interval, o.MaxInterval
	if interval <= 0 {
		interval = 2 * time.Second
	}
	if maxInterval <= 0 {
		maxInterval = 10 * time.Second
	}
	started := time.Now()
	deadline := started.Add(defaultWaitTimeout)
	if o.Timeout > 0 {
		deadline = started.Add(o.Timeout)
	}
	adopted := o.Timeout > 0
	var last *Job
	for {
		job, err := j.Get(ctx, id)
		if err != nil {
			if ce := contextError(ctx, err, id, "", last); ce != nil {
				return last, ce
			}
			return last, err
		}
		last = job
		if o.OnProgress != nil {
			o.OnProgress(job)
		}
		if job.Status().terminal() {
			return job, nil
		}
		if limit := job.info.Timeout; !adopted && limit > 0 {
			adopted = true
			// The budget runs from about now, not from when Wait started.
			if own := time.Now().Add(limit + waitGrace); own.After(deadline) {
				deadline = own
			}
		}
		left := time.Until(deadline)
		if left <= 0 {
			return job, &WaitTimeoutError{JobID: id, LastStatus: job.Status(), Waited: time.Since(started), Job: job}
		}
		timer := time.NewTimer(min(interval, left))
		select {
		case <-ctx.Done():
			timer.Stop()
			return job, contextError(ctx, ctx.Err(), id, "", job)
		case <-timer.C:
		}
		interval = min(interval*3/2, maxInterval)
	}
}

// Run submits a job and waits for it. A failed or cancelled job returns
// *JobFailedError with the job.
//
// Once the job is submitted, every error Wait returns comes back with the last
// job Run read, or nil before the first read. A *ContextError or
// *WaitTimeoutError also names the job id, so you can call Jobs.Get,
// Jobs.Wait or Jobs.Cancel with it. A *ContextError from Run also carries
// the idempotency key the submit sent.
func (j *Jobs) Run(ctx context.Context, p CreateParams, o *WaitOptions) (*Job, error) {
	if p.IdempotencyKey == "" {
		p.IdempotencyKey = GenerateIdempotencyKey() // p is a copy, so the caller's params stay as they were
	}
	created, err := j.Create(ctx, p)
	if err != nil {
		return nil, err
	}
	job, err := j.Wait(ctx, created.ID, o)
	var ce *ContextError
	if errors.As(err, &ce) {
		ce.IdempotencyKey = p.IdempotencyKey
	}
	if err != nil {
		return job, err
	}
	if job.Status() != StatusComplete {
		return job, &JobFailedError{Job: job}
	}
	return job, nil
}

// Account reads what this API key's workspace may do.
type Account struct{ client *Client }

// Capabilities returns plan limits and enabled features. It contains no billing data.
func (a *Account) Capabilities(ctx context.Context) (*AccountCapabilities, error) {
	if err := a.client.needKey(); err != nil {
		return nil, err
	}
	res, err := a.client.core.Account.GetAccountCapabilities(ctx)
	if err != nil {
		return nil, fromGenerated(err)
	}
	return convert[AccountCapabilities](res.Data)
}
