package rendobar

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"iter"
	"net/url"
	"strconv"
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

// CreateParams is a job submit. Params is required: the typed params of one
// job type, such as FfmpegParams, or CustomParams for a type this SDK predates.
type CreateParams struct {
	// Params are the job's params. Their JobType is the job type submitted.
	Params JobParams
	// Inputs maps an input name to its value: a URL, a list of URLs, inline
	// Content, a FromJob reference or Fonts (see Input). Jobs.Schema lists the
	// names a type takes, such as "source", or "images" for image.edit.
	// ffmpeg and ffprobe accept any file name, and the command refers to the
	// file by it. A command that names its input URLs needs no Inputs.
	Inputs map[string]Input
	// IdempotencyKey makes the submit safe to repeat. With none, the SDK
	// generates one per Create call, so its own retries cannot create a second job.
	// Reusing a key with different params is refused with a 409 *Error, code CodeConflict.
	IdempotencyKey string
	// Label is a short name for the job, up to 200 characters, such as "Trim
	// intro for episode 12". The dashboard shows it, and JobInfo.Label returns it.
	Label string
	// Destinations are your storage connections to deliver the output to once
	// the job completes, as "storage://<id>", "storage://<id>/<folder>" or
	// "storage://<id>/<path template>". A path template may use {job_id},
	// {ext}, {source_name} and {date}. JobInfo.Deliveries reports each one.
	Destinations []string
	// Callback asks the API to POST the job's result to a URL of yours when
	// the job ends, or nil.
	Callback *Callback
	// ForceAsync makes the submit return as soon as the job is queued, even
	// for a job type the API could finish within the submit request.
	ForceAsync bool
}

// Callback is a per-job completion callback. When the job completes, fails or
// is cancelled, the API POSTs the standard webhook envelope to URL (see the
// webhooks package).
type Callback struct {
	URL string // an HTTPS URL
	// Headers are sent with the POST, such as an Authorization bearer for an
	// authenticated endpoint. X-Rendobar-* headers are reserved.
	Headers map[string]string
	// Verify signs the POST with X-Rendobar-Signature, the webhook scheme,
	// keyed with your workspace's callback signing secret. Unsigned by default.
	Verify bool
	// Events adds non-terminal events. The terminal ones always fire.
	Events []CallbackEvent
}

func (cb *Callback) validate(c *check) {
	if cb.URL == "" {
		c.fail("callback.url", "is required")
	}
	for _, e := range cb.Events {
		if !e.known() {
			c.fail("callback.events", fmt.Sprintf("%q is not one of the known events", e))
		}
	}
}

func (cb *Callback) wire() *api.SubmitAnyJobCallback {
	out := &api.SubmitAnyJobCallback{URL: cb.URL, Headers: cb.Headers}
	if cb.Verify {
		out.Verify = &cb.Verify
	}
	for _, e := range cb.Events {
		out.Events = append(out.Events, api.SubmitAnyJobCallbackEventsItem(e))
	}
	return out
}

// Created is what a submit returns: the job id and its status at that moment.
type Created struct {
	ID     string    `json:"id"`
	Status JobStatus `json:"status"`
	// Replayed reports that an earlier submit with the same idempotency key
	// already created this job, so this call created nothing new.
	Replayed bool `json:"idempotent"`
	// URL is the result as a data URL when the API finished the job within
	// the submit request (Status complete), else "".
	URL string `json:"url"`
}

// GenerateIdempotencyKey returns "sdk_" plus 32 hex characters: unique per call, not secret.
func GenerateIdempotencyKey() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(fmt.Sprintf("rendobar: crypto/rand failed: %v", err))
	}
	return "sdk_" + hex.EncodeToString(b[:])
}

// jsonObject encodes typed params or inputs as the JSON object the API takes.
// Numbers stay json.Number, so an int64 above 2^53 keeps every digit.
func jsonObject(what string, v any) (map[string]any, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, local(CodeInvalidArgument, "%s do not encode to JSON: %v", what, err)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var m map[string]any
	if err := dec.Decode(&m); err != nil {
		return nil, local(CodeInvalidArgument, "%s must encode to a JSON object: %v", what, err)
	}
	return m, nil
}

// Create submits a job and returns at once. Safe to retry: one key rides every retry of this call.
//
// When the submit fails in a way that may have left a job behind (ctx ended,
// a connection error, a timed-out attempt, or a 429 or 5xx after the last
// retry), Create returns a *SubmitError with the idempotency key it sent.
// Submit again with that key to get the same job.
//
// Create validates the params first (JobParams.Validate) and sends nothing
// when they are wrong.
func (j *Jobs) Create(ctx context.Context, p CreateParams) (*Created, error) {
	if err := j.client.needKey(); err != nil {
		return nil, err
	}
	if p.Params == nil {
		return nil, local(CodeInvalidArgument, "CreateParams.Params is required: use the job type's params struct, such as FfmpegParams")
	}
	if err := p.Params.Validate(); err != nil {
		return nil, err
	}
	var c check
	checkInputs(p.Inputs, &c)
	if p.Callback != nil {
		p.Callback.validate(&c)
	}
	if err := c.err(); err != nil {
		return nil, err
	}
	params, err := jsonObject("params", p.Params)
	if err != nil {
		return nil, err
	}
	inputs, err := jsonObject("inputs", p.Inputs)
	if err != nil {
		return nil, err
	}
	key := p.IdempotencyKey
	if key == "" {
		key = GenerateIdempotencyKey()
	}
	body := &api.SubmitAnyJob{Type: string(p.Params.JobType()), Params: params, Inputs: inputs, IdempotencyKey: &key, Destinations: p.Destinations}
	if p.Callback != nil {
		body.Callback = p.Callback.wire()
	}
	if p.Label != "" {
		body.Label = &p.Label
	}
	if p.ForceAsync {
		body.ForceAsync = &p.ForceAsync
	}
	res, err := j.client.core.Jobs.SubmitJob(ctx, &api.SubmitJobRequest{SubmitAnyJob: body})
	if err != nil {
		return nil, submitError(ctx, err, key)
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
	if err := checkID(id); err != nil {
		return nil, err
	}
	var res struct {
		Data *Job `json:"data"`
	}
	if err := j.client.getJSON(ctx, "/jobs/"+url.PathEscape(id), nil, &res); err != nil {
		return nil, err
	}
	if res.Data == nil {
		return nil, fmt.Errorf("rendobar: response has no job")
	}
	return res.Data, nil
}

// ListParams filters Jobs.List and Jobs.All. The zero value lists every job.
type ListParams struct {
	// Status keeps jobs in one status, such as StatusFailed. "" keeps all.
	Status JobStatus
	// Type keeps jobs of one type, such as JobTypeFfmpeg. "" keeps all.
	Type JobType
	// Limit is the page size, up to 100. The API treats a larger value as 100.
	// 0 uses the API's default of 50 for List. All always fetches pages of
	// 100 or of a smaller Limit you set. A negative Limit is an INVALID_ARGUMENT *Error.
	Limit int
	// Offset skips that many matching jobs. A negative Offset is an INVALID_ARGUMENT *Error.
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
		return nil, local(CodeInvalidArgument, "ListParams Limit and Offset must not be negative, got %d and %d", p.Limit, p.Offset)
	}
	q := url.Values{}
	if p.Status != "" {
		q.Set("status", string(p.Status))
	}
	if p.Type != "" {
		q.Set("type", string(p.Type))
	}
	if p.Limit > 0 {
		q.Set("limit", strconv.Itoa(p.Limit))
	}
	if p.Offset > 0 {
		q.Set("offset", strconv.Itoa(p.Offset))
	}
	var res struct {
		Data []*Job `json:"data"`
		Meta *struct {
			Total int64 `json:"total"`
		} `json:"meta"`
	}
	if err := j.client.getJSON(ctx, "/jobs", q, &res); err != nil {
		return nil, err
	}
	page := &Page{Jobs: res.Data}
	if page.Jobs == nil {
		page.Jobs = []*Job{}
	}
	if res.Meta != nil {
		page.Total = int(res.Meta.Total)
	}
	return page, nil
}

// All yields every matching job, fetching pages as you range over it.
//
//	for job, err := range rb.Jobs.All(ctx, rendobar.ListParams{Status: rendobar.StatusFailed}) { ... }
func (j *Jobs) All(ctx context.Context, p ListParams) iter.Seq2[*Job, error] {
	return func(yield func(*Job, error) bool) {
		q := p // a copy per range, so ranging the same sequence twice yields everything twice
		if q.Limit <= 0 || q.Limit > 100 {
			q.Limit = 100 // the API's largest page
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
	if err := checkID(id); err != nil {
		return err
	}
	_, err := j.client.core.Jobs.CancelJob(ctx, &api.CancelJobRequest{ID: id})
	return fromGenerated(err)
}

// Types returns every live job type. Public: needs no API key.
func (j *Jobs) Types(ctx context.Context) ([]JobTypeInfo, error) {
	if j.client.confErr != nil {
		return nil, j.client.confErr
	}
	res, err := j.client.core.Jobs.ListJobTypes(ctx)
	if err != nil {
		return nil, fromGenerated(err)
	}
	types, err := convert[[]JobTypeInfo](res.Data)
	if err != nil {
		return nil, err
	}
	return *types, nil
}

// Schema returns one job type's params: its fields, inputs and full JSON Schema. Public: needs no API key.
func (j *Jobs) Schema(ctx context.Context, jobType JobType) (*JobTypeSchema, error) {
	if j.client.confErr != nil {
		return nil, j.client.confErr
	}
	if !jobTypeName.MatchString(string(jobType)) {
		return nil, local(CodeInvalidArgument, "job type %q is not valid: use the name Jobs.Types lists, such as ffmpeg", jobType)
	}
	res, err := j.client.core.Jobs.GetJobTypeSchema(ctx, &api.GetJobTypeSchemaRequest{Type: string(jobType)})
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
			if ce := contextError(ctx, err, id, last); ce != nil {
				return last, ce
			}
			return last, err
		}
		last = job
		if o.OnProgress != nil {
			o.OnProgress(job)
		}
		if job.IsTerminal() {
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
			return job, contextError(ctx, ctx.Err(), id, job)
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
// Jobs.Wait or Jobs.Cancel with it. A failed submit returns what Jobs.Create
// returns, such as a *SubmitError with the idempotency key.
func (j *Jobs) Run(ctx context.Context, p CreateParams, o *WaitOptions) (*Job, error) {
	created, err := j.Create(ctx, p)
	if err != nil {
		return nil, err
	}
	job, err := j.Wait(ctx, created.ID, o)
	if err != nil {
		return job, err
	}
	if job.Status() != StatusComplete {
		return job, jobFailed(job)
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
	var res struct {
		Data *AccountCapabilities `json:"data"`
	}
	if err := a.client.getJSON(ctx, "/account/capabilities", nil, &res); err != nil {
		return nil, err
	}
	if res.Data == nil {
		return nil, fmt.Errorf("rendobar: response has no capabilities")
	}
	return res.Data, nil
}
