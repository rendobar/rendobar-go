package rendobar

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"iter"
	"time"

	"github.com/rendobar/rendobar-go/internal/api"
)

// Job is a job in any status. Exactly one of Waiting, Dispatched, Running,
// Complete, Failed or Cancelled is set, matching Status. A status newer than
// this SDK leaves all of them nil and keeps Status, so code can still branch on it.
type Job = api.Job

// JobType is one live job type, from Jobs.Types.
type JobType = api.JobType

// AccountCapabilities is what this API key's workspace may do.
type AccountCapabilities = api.GetAccountCapabilitiesResponseData

const (
	defaultWaitTimeout = 5 * time.Minute  // until the job reports its own budget
	waitGrace          = 2 * time.Minute  // headroom past that budget for upload and callback
)

// Jobs submits jobs, follows them to the end, and reads their output.
type Jobs struct{ client *Client }

// CreateParams is a job submit. Params is the job type's params: a map, or any
// struct that encodes to the JSON the type's schema describes (see Jobs.Schema).
type CreateParams struct {
	Type   string
	Params any
	Inputs map[string]any
	// IdempotencyKey makes the submit safe to repeat. With none, the SDK
	// generates one per Create call, so its own retries cannot create a second job.
	IdempotencyKey string
	Label          string
	Destinations   []string
	ForceAsync     bool
}

// Created is what a submit returns: the job id and its status at that moment.
type Created struct {
	ID     string `json:"id"`
	Status string `json:"status"`
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
func (j *Jobs) Create(ctx context.Context, p CreateParams) (*Created, error) {
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
	res, err := j.client.core.Jobs.GetJob(ctx, &api.GetJobRequest{ID: id})
	if err != nil {
		return nil, fromGenerated(err)
	}
	return res.Data, nil
}

// ListParams filters Jobs.List and Jobs.All.
type ListParams struct {
	Status string
	Type   string
	Limit  int
	Offset int
}

// Page is one page of jobs, newest first, and the total that matched.
type Page struct {
	Jobs  []*Job
	Total int
}

// List returns one page of jobs.
func (j *Jobs) List(ctx context.Context, p ListParams) (*Page, error) {
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
	page := &Page{Jobs: res.Data}
	if res.Meta != nil {
		page.Total = res.Meta.Total
	}
	return page, nil
}

// All yields every matching job, fetching pages as you range over it.
//
//	for job, err := range rb.Jobs.All(ctx, rendobar.ListParams{Status: "failed"}) { ... }
func (j *Jobs) All(ctx context.Context, p ListParams) iter.Seq2[*Job, error] {
	return func(yield func(*Job, error) bool) {
		if p.Limit <= 0 {
			p.Limit = 100
		}
		for {
			page, err := j.List(ctx, p)
			if err != nil {
				yield(nil, err)
				return
			}
			for _, job := range page.Jobs {
				if !yield(job, nil) {
					return
				}
			}
			p.Offset += len(page.Jobs)
			if len(page.Jobs) == 0 || p.Offset >= page.Total {
				return
			}
		}
	}
}

// Cancel cancels a job that has not finished.
func (j *Jobs) Cancel(ctx context.Context, id string) error {
	_, err := j.client.core.Jobs.CancelJob(ctx, &api.CancelJobRequest{ID: id})
	return fromGenerated(err)
}

// Download returns the output file of a completed job.
func (j *Jobs) Download(ctx context.Context, id string) ([]byte, error) {
	r, err := j.client.core.Jobs.DownloadJobOutput(ctx, &api.DownloadJobOutputRequest{ID: id})
	if err != nil {
		return nil, fromGenerated(err)
	}
	var buf bytes.Buffer
	if _, err := io.Copy(&buf, r); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// Types returns every live job type. Public: needs no API key.
func (j *Jobs) Types(ctx context.Context) ([]*JobType, error) {
	res, err := j.client.core.Jobs.ListJobTypes(ctx)
	if err != nil {
		return nil, fromGenerated(err)
	}
	return res.Data, nil
}

// Schema returns one job type's params: its fields, inputs and full JSON Schema. Public: needs no API key.
func (j *Jobs) Schema(ctx context.Context, jobType string) (*api.GetJobTypeSchemaResponseData, error) {
	res, err := j.client.core.Jobs.GetJobTypeSchema(ctx, &api.GetJobTypeSchemaRequest{Type: jobType})
	if err != nil {
		return nil, fromGenerated(err)
	}
	return res.Data, nil
}

// WaitOptions tune Jobs.Wait and Jobs.Run. The zero value is fine.
type WaitOptions struct {
	// Timeout overrides the deadline. By default it is 5 minutes, then the job's
	// own time limit plus 2 minutes once the job reports one.
	Timeout     time.Duration
	Interval    time.Duration // first poll gap, default 2s
	MaxInterval time.Duration // longest poll gap, default 10s
	OnProgress  func(*Job)
}

func terminal(status string) bool {
	return status == "complete" || status == "failed" || status == "cancelled"
}

func budget(job *Job) *int {
	switch {
	case job.Waiting != nil:
		return job.Waiting.TimeoutMs
	case job.Dispatched != nil:
		return job.Dispatched.TimeoutMs
	case job.Running != nil:
		return job.Running.TimeoutMs
	}
	return nil
}

// Wait polls until the job completes, fails or is cancelled, and returns it.
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
	for {
		job, err := j.Get(ctx, id)
		if err != nil {
			return nil, err
		}
		if o.OnProgress != nil {
			o.OnProgress(job)
		}
		if terminal(job.Status) {
			return job, nil
		}
		if ms := budget(job); !adopted && ms != nil {
			adopted = true
			if own := started.Add(time.Duration(*ms)*time.Millisecond + waitGrace); own.After(deadline) {
				deadline = own
			}
		}
		left := time.Until(deadline)
		if left <= 0 {
			return nil, &WaitTimeoutError{JobID: id, LastStatus: job.Status, Waited: deadline.Sub(started).Seconds()}
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(min(interval, left)):
		}
		interval = min(interval*3/2, maxInterval)
	}
}

// Run submits a job and waits for it. A failed or cancelled job returns *JobFailedError with the job.
func (j *Jobs) Run(ctx context.Context, p CreateParams, o *WaitOptions) (*Job, error) {
	created, err := j.Create(ctx, p)
	if err != nil {
		return nil, err
	}
	job, err := j.Wait(ctx, created.ID, o)
	if err != nil {
		return nil, err
	}
	if job.Status != "complete" {
		return job, &JobFailedError{Job: job}
	}
	return job, nil
}

// Account reads what this API key's workspace may do.
type Account struct{ client *Client }

// Capabilities returns plan limits and enabled features. It contains no billing data.
func (a *Account) Capabilities(ctx context.Context) (*AccountCapabilities, error) {
	res, err := a.client.core.Account.GetAccountCapabilities(ctx)
	if err != nil {
		return nil, fromGenerated(err)
	}
	return res.Data, nil
}
