package rendobar

import (
	"bytes"
	"encoding/json"
	"fmt"
	"time"

	"github.com/rendobar/rendobar-go/internal/api"
)

// Job is a job in any status. Exactly one of Waiting, Dispatched, Running,
// Complete, Failed or Cancelled is set, matching Status. A status newer than
// this SDK leaves all six nil, and the methods still answer.
//
// The methods read the fields every status shares, so a list of jobs needs no
// type switch:
//
//	for job, err := range rb.Jobs.All(ctx, rendobar.ListParams{}) {
//		if err != nil {
//			return err
//		}
//		fmt.Println(job.ID(), job.Type(), job.Status(), job.CreatedAt())
//	}
type Job struct {
	Waiting    *JobWaiting
	Dispatched *JobDispatched
	Running    *JobRunning
	Complete   *JobComplete
	Failed     *JobFailed
	Cancelled  *JobCancelled

	status   JobStatus
	terminal bool
	info     JobInfo
}

// IsTerminal reports whether the job has finished and will not change
// status again. For a status newer than this SDK it reads the terminal flag
// the API sends on every job, so Wait stops on a final status it does not know.
func (j *Job) IsTerminal() bool { return j.terminal }

// ID is the job id, such as job_abc123.
func (j *Job) ID() string { return j.info.ID }

// Type is the job type, such as ffmpeg or ffprobe.
func (j *Job) Type() string { return j.info.Type }

// Status is the job's status when it was read.
func (j *Job) Status() JobStatus { return j.status }

// CreatedAt is when the job was submitted.
func (j *Job) CreatedAt() time.Time { return j.info.CreatedAt }

// Info returns every field the statuses share, such as Label, Cost and
// WebURL. Each status type embeds the same JobInfo.
func (j *Job) Info() JobInfo { return j.info }

// UnmarshalJSON reads a job as the API sends it, the data of GET /jobs/{id}.
// Unknown fields are ignored and an unknown status sets no variant.
func (j *Job) UnmarshalJSON(b []byte) error {
	var w wireJob
	if err := json.Unmarshal(b, &w); err != nil {
		return fmt.Errorf("rendobar: unreadable job: %w", err)
	}
	*j = *w.job()
	return nil
}

// JobInfo holds what a job carries in every status. Each status type embeds
// it, so job.Complete.ID and job.Failed.CreatedAt read it directly. A time
// the job has not reached yet is the zero time.Time.
type JobInfo struct {
	ID    string // job id, such as job_abc123
	OrgID string // the workspace that owns the job
	Type  string // job type, such as ffmpeg
	Label string // the name given at submit, or ""

	Source         string          // how the job was created: api, dashboard, mcp and others
	Client         string          // the client that sent it, such as sdk, cli or n8n, or ""
	IdempotencyKey string          // the key the submit carried, or ""
	Params         json.RawMessage // the params as submitted, a JSON object
	Inputs         json.RawMessage // the input files as submitted, a JSON object

	// MediaType is the detected media of the input: video, image or audio, or "".
	MediaType string
	// OutputCategory is the kind of output the job makes.
	OutputCategory OutputCategory
	// Model is the generation model an image.generate or image.edit job resolved to, or "".
	Model string
	// Region is where the job runs, such as us-east-1, or "" until it is dispatched.
	Region string
	// Timeout is how long the job may run: the requested timeout capped by
	// your plan. 0 until the job is dispatched.
	Timeout time.Duration
	// Cost is what the job was charged, or nil until it is billed.
	Cost *Cost
	// RetryCount is how many times the job was retried.
	RetryCount int
	// Steps are the job's execution steps in order.
	Steps []JobStep
	// LogsAvailable reports whether execution logs are stored.
	LogsAvailable bool
	// ResourcesAvailable reports whether CPU and memory usage over time is stored.
	ResourcesAvailable bool
	// CallbackStatus is the delivery state of the callback set at submit, or
	// "" when the job has no callback.
	CallbackStatus CallbackStatus
	// Deliveries lists where the output was or will be delivered, one entry per
	// destination given at submit. Only Jobs.Get fills it.
	Deliveries []Delivery
	// WebURL is the job's page in the dashboard.
	WebURL string

	CreatedAt    time.Time // when the job was submitted
	DispatchedAt time.Time // when a runner took it
	StartedAt    time.Time // when it started running
	CompletedAt  time.Time // when it finished
	SettledAt    time.Time // when the API recorded its final status
	// RetentionExpiresAt is when the stored output files are deleted, set
	// once the job completes with stored output. Output.ExpiresAt is the much
	// earlier expiry of the download URLs.
	RetentionExpiresAt time.Time
}

// JobWaiting is a job queued and not yet dispatched (Job.Waiting).
type JobWaiting struct {
	JobInfo
}

// JobDispatched is a job handed to a runner that has not started (Job.Dispatched).
type JobDispatched struct {
	JobInfo
	// Progress is the completed fraction, 0 to 1, or nil when unknown.
	Progress *float64
	// ETA is the estimated time left, or 0 when unknown.
	ETA time.Duration
	// Boot describes the runner starting up, or nil.
	Boot *BootState
}

// JobRunning is a job in progress (Job.Running).
type JobRunning struct {
	JobInfo
	// Progress is the completed fraction, 0 to 1, or nil when unknown.
	Progress *float64
	// ETA is the estimated time left, or 0 when unknown.
	ETA time.Duration
}

// JobComplete is a finished job with its output (Job.Complete).
type JobComplete struct {
	JobInfo
	Output JobOutput
}

// JobFailed is a job that failed, with its error (Job.Failed).
type JobFailed struct {
	JobInfo
	Error JobError
}

// JobCancelled is a job that was cancelled (Job.Cancelled).
type JobCancelled struct {
	JobInfo
}

// JobOutput is what a completed job produced. A data job such as ffprobe
// fills Data and has no files. A file job such as ffmpeg fills Files, and File
// when it has one headline result.
type JobOutput struct {
	// Data is the job type's computed result as JSON, or nil when the job
	// produced none. JobComplete has typed readers for known shapes, such as
	// JobComplete.Ffprobe.
	Data json.RawMessage
	// File is the single headline file: the one output file, or the stream
	// manifest (.m3u8 or .mpd). nil for data jobs and for plain sets of files.
	File *OutputFile
	// Files is every file the job produced. Empty for data jobs.
	Files []OutputFile
	// ExpiresAt is when the file URLs stop working. Zero when there are no files.
	ExpiresAt time.Time
}

// UnmarshalJSON reads a job output as the API sends it.
func (o *JobOutput) UnmarshalJSON(b []byte) error {
	var w wireOutput
	if err := json.Unmarshal(b, &w); err != nil {
		return err
	}
	*o = JobOutput{Data: present(w.Data), ExpiresAt: unixMs(w.ExpiresAt)}
	if w.File != nil {
		f := w.File.file()
		o.File = &f
	}
	for _, f := range w.Files {
		o.Files = append(o.Files, f.file())
	}
	return nil
}

// OutputFile is one file a job produced.
type OutputFile struct {
	URL  string   // a ready-to-fetch URL that expires at JobOutput.ExpiresAt
	Path string   // the path within the job output, such as the name an ffmpeg command wrote
	Type FileType // the file type, from its extension
	Size int64    // bytes
	// SHA256 is the file's digest as 64 lowercase hex characters, or "" on
	// jobs completed before October 2026.
	SHA256 string

	// Media details, each zero when unknown.
	Format   string        // container format, from the extension
	Width    int           // pixels
	Height   int           // pixels
	Duration time.Duration // media duration
	Seed     int64         // the seed a generation used, for reproducing it
}

// JobError is why a job failed (JobFailed.Error).
type JobError struct {
	// Code is the job's error code, such as INPUT_UNREADABLE. Job codes are
	// their own open set, apart from the API's ErrorCode values.
	Code      string
	Message   string       // human-readable explanation
	Detail    string       // the tail of the tool's stderr, such as ffmpeg's, or ""
	Retryable bool         // true when running the same job again may succeed
	Phase     FailurePhase // the phase the job failed in, or "" when unknown
}

// UnmarshalJSON reads a job error as the API sends it.
func (e *JobError) UnmarshalJSON(b []byte) error {
	var w wireJobError
	if err := json.Unmarshal(b, &w); err != nil {
		return err
	}
	*e = JobError{Code: w.Code, Message: w.Message, Detail: str(w.Detail), Retryable: w.Retryable, Phase: FailurePhase(str(w.FailedPhase))}
	return nil
}

// Cost is what a job was charged.
type Cost struct {
	Nanodollars int64    // the amount, where 1 USD is 1,000,000,000
	Currency    Currency // ISO 4217 code
	Formatted   string   // the amount for display, such as $0.50
}

// UnmarshalJSON reads a cost as the API sends it.
func (c *Cost) UnmarshalJSON(b []byte) error {
	var w wireCost
	if err := json.Unmarshal(b, &w); err != nil {
		return err
	}
	*c = Cost{Nanodollars: w.Amount, Currency: Currency(w.Currency), Formatted: w.Formatted}
	return nil
}

// JobStep is one execution step of a job.
type JobStep struct {
	ID     string
	Name   string
	Status StepStatus
	Error  string          // why the step failed, or ""
	Meta   json.RawMessage // step details as the API sent them, or nil

	StartedAt   time.Time
	CompletedAt time.Time
	Duration    time.Duration
}

// UnmarshalJSON reads a step as the API sends it.
func (s *JobStep) UnmarshalJSON(b []byte) error {
	var w wireStep
	if err := json.Unmarshal(b, &w); err != nil {
		return err
	}
	*s = JobStep{
		ID: w.ID, Name: w.Name, Status: StepStatus(w.Status), Error: str(w.Error), Meta: present(w.Meta),
		StartedAt: unixMs(w.StartedAt), CompletedAt: unixMs(w.CompletedAt), Duration: ms(w.DurationMs),
	}
	return nil
}

// Delivery is one destination a job's output goes to.
type Delivery struct {
	StorageID string // the storage connection
	Status    DeliveryStatus
	// Path is the object key in the bucket. A folder of files ends in /.
	Path string
	// URL is where to fetch the delivered file, or "" for a folder.
	URL string
	// Reason is why a delivery failed, or "".
	Reason DeliveryReason
	// Renamed reports that the name was taken, so the job id was appended.
	Renamed bool
}

// UnmarshalJSON reads a delivery as the API sends it.
func (d *Delivery) UnmarshalJSON(b []byte) error {
	var w wireDelivery
	if err := json.Unmarshal(b, &w); err != nil {
		return err
	}
	*d = Delivery{
		StorageID: w.StorageID, Status: DeliveryStatus(w.Status), Path: str(w.Path), URL: str(w.URL),
		Reason: DeliveryReason(str(w.Reason)), Renamed: w.Renamed != nil && *w.Renamed,
	}
	return nil
}

// BootState describes a runner starting up for a dispatched job.
type BootState struct {
	Phase BootPhase
	// Detail is a human-readable line, such as "waiting for GPU capacity".
	Detail  string
	Elapsed time.Duration // since dispatch
	Typical time.Duration // a typical start-up for this workload, or 0 when unknown
	ETA     time.Duration // estimated time left for the whole job, or 0 when unknown
}

// The wire shape of a job. Job.UnmarshalJSON reads this, then builds the
// public types, which never expose generated ones.
type wireJob struct {
	ID                 string          `json:"id"`
	OrgID              string          `json:"orgId"`
	Type               string          `json:"type"`
	Status             string          `json:"status"`
	Terminal           *bool           `json:"terminal"`
	Label              *string         `json:"label"`
	Source             string          `json:"source"`
	Client             *string         `json:"client"`
	IdempotencyKey     *string         `json:"idempotencyKey"`
	Params             json.RawMessage `json:"params"`
	Inputs             json.RawMessage `json:"inputs"`
	MediaType          *string         `json:"mediaType"`
	OutputCategory     string          `json:"outputCategory"`
	Model              *string         `json:"model"`
	Region             *string         `json:"region"`
	TimeoutMs          *int64          `json:"timeoutMs"`
	Cost               *Cost           `json:"cost"`
	RetryCount         int             `json:"retryCount"`
	Steps              []JobStep       `json:"steps"`
	LogsAvailable      bool            `json:"logsAvailable"`
	ResourcesAvailable bool            `json:"resourcesAvailable"`
	Callback           *struct {
		Status string `json:"status"`
	} `json:"callback"`
	Deliveries         []Delivery `json:"deliveries"`
	WebURL             string     `json:"webUrl"`
	CreatedAt          *int64     `json:"createdAt"`
	DispatchedAt       *int64     `json:"dispatchedAt"`
	StartedAt          *int64     `json:"startedAt"`
	CompletedAt        *int64     `json:"completedAt"`
	SettledAt          *int64     `json:"settledAt"`
	RetentionExpiresAt *int64     `json:"retentionExpiresAt"`

	Progress  *float64       `json:"progress"`
	Eta       *float64       `json:"eta"`
	BootState *wireBootState `json:"bootState"`
	Output    *JobOutput     `json:"output"`
	Error     *JobError      `json:"error"`
}

type wireCost struct {
	Amount    int64  `json:"amount"`
	Currency  string `json:"currency"`
	Formatted string `json:"formatted"`
}

type wireStep struct {
	ID          string          `json:"id"`
	Name        string          `json:"name"`
	Status      string          `json:"status"`
	Error       *string         `json:"error"`
	Meta        json.RawMessage `json:"meta"`
	StartedAt   *int64          `json:"startedAt"`
	CompletedAt *int64          `json:"completedAt"`
	DurationMs  *int64          `json:"durationMs"`
}

type wireDelivery struct {
	StorageID string  `json:"storageId"`
	Status    string  `json:"status"`
	Path      *string `json:"path"`
	URL       *string `json:"url"`
	Reason    *string `json:"reason"`
	Renamed   *bool   `json:"renamed"`
}

type wireBootState struct {
	Phase     string `json:"phase"`
	Detail    string `json:"detail"`
	ElapsedMs int64  `json:"elapsedMs"`
	TypicalMs *int64 `json:"typicalMs"`
	EtaMs     *int64 `json:"etaMs"`
}

type wireOutput struct {
	Data      json.RawMessage `json:"data"`
	File      *wireFile       `json:"file"`
	Files     []wireFile      `json:"files"`
	ExpiresAt *int64          `json:"expiresAt"`
}

type wireFile struct {
	URL    string  `json:"url"`
	Path   string  `json:"path"`
	Type   string  `json:"type"`
	Size   int64   `json:"size"`
	Sha256 *string `json:"sha256"`
	Meta   *struct {
		Format     *string `json:"format"`
		Width      *int64  `json:"width"`
		Height     *int64  `json:"height"`
		DurationMs *int64  `json:"durationMs"`
		Seed       *int64  `json:"seed"`
	} `json:"meta"`
}

type wireJobError struct {
	Code        string  `json:"code"`
	Message     string  `json:"message"`
	Detail      *string `json:"detail"`
	Retryable   bool    `json:"retryable"`
	FailedPhase *string `json:"failedPhase"`
}

// fromAPIJob turns the generated core's job into the public Job, through its JSON.
func fromAPIJob(in *api.Job) (*Job, error) {
	if in == nil {
		return nil, fmt.Errorf("rendobar: response has no job")
	}
	raw, err := json.Marshal(in)
	if err != nil {
		return nil, fmt.Errorf("rendobar: unreadable job: %w", err)
	}
	job := new(Job)
	if err := job.UnmarshalJSON(raw); err != nil {
		return nil, err
	}
	return job, nil
}

func (w *wireJob) job() *Job {
	info := JobInfo{
		ID:                 w.ID,
		OrgID:              w.OrgID,
		Type:               w.Type,
		Label:              str(w.Label),
		Source:             w.Source,
		Client:             str(w.Client),
		IdempotencyKey:     str(w.IdempotencyKey),
		Params:             present(w.Params),
		Inputs:             present(w.Inputs),
		MediaType:          str(w.MediaType),
		OutputCategory:     OutputCategory(w.OutputCategory),
		Model:              str(w.Model),
		Region:             str(w.Region),
		Timeout:            ms(w.TimeoutMs),
		Cost:               w.Cost,
		RetryCount:         w.RetryCount,
		Steps:              w.Steps,
		LogsAvailable:      w.LogsAvailable,
		ResourcesAvailable: w.ResourcesAvailable,
		Deliveries:         w.Deliveries,
		WebURL:             w.WebURL,
		CreatedAt:          unixMs(w.CreatedAt),
		DispatchedAt:       unixMs(w.DispatchedAt),
		StartedAt:          unixMs(w.StartedAt),
		CompletedAt:        unixMs(w.CompletedAt),
		SettledAt:          unixMs(w.SettledAt),
		RetentionExpiresAt: unixMs(w.RetentionExpiresAt),
	}
	if w.Callback != nil {
		info.CallbackStatus = CallbackStatus(w.Callback.Status)
	}

	job := &Job{status: JobStatus(w.Status), info: info}
	switch job.status {
	case StatusComplete, StatusFailed, StatusCancelled:
		job.terminal = true
	case StatusWaiting, StatusDispatched, StatusRunning:
		job.terminal = false
	default:
		// A status newer than this SDK: only the API knows whether it is final.
		job.terminal = w.Terminal != nil && *w.Terminal
	}
	switch job.status {
	case StatusWaiting:
		job.Waiting = &JobWaiting{JobInfo: info}
	case StatusDispatched:
		job.Dispatched = &JobDispatched{JobInfo: info, Progress: w.Progress, ETA: secs(w.Eta)}
		if b := w.BootState; b != nil {
			job.Dispatched.Boot = &BootState{
				Phase: BootPhase(b.Phase), Detail: b.Detail,
				Elapsed: ms(&b.ElapsedMs), Typical: ms(b.TypicalMs), ETA: ms(b.EtaMs),
			}
		}
	case StatusRunning:
		job.Running = &JobRunning{JobInfo: info, Progress: w.Progress, ETA: secs(w.Eta)}
	case StatusComplete:
		job.Complete = &JobComplete{JobInfo: info}
		if w.Output != nil {
			job.Complete.Output = *w.Output
		}
	case StatusFailed:
		job.Failed = &JobFailed{JobInfo: info}
		if w.Error != nil {
			job.Failed.Error = *w.Error
		}
	case StatusCancelled:
		job.Cancelled = &JobCancelled{JobInfo: info}
	default:
		// A status newer than this SDK: no variant, but Status and Info still answer.
	}
	return job
}

func (f wireFile) file() OutputFile {
	out := OutputFile{URL: f.URL, Path: f.Path, Type: FileType(f.Type), Size: f.Size, SHA256: str(f.Sha256)}
	if m := f.Meta; m != nil {
		out.Format = str(m.Format)
		out.Width = int(integer(m.Width))
		out.Height = int(integer(m.Height))
		out.Duration = ms(m.DurationMs)
		out.Seed = integer(m.Seed)
	}
	return out
}

// present returns raw, or nil when it is absent or JSON null.
func present(raw json.RawMessage) json.RawMessage {
	if t := bytes.TrimSpace(raw); len(t) == 0 || string(t) == "null" {
		return nil
	}
	return raw
}

func str(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func num(p *float64) float64 {
	if p == nil {
		return 0
	}
	return *p
}

func integer(p *int64) int64 {
	if p == nil {
		return 0
	}
	return *p
}

func ms(p *int64) time.Duration { return time.Duration(integer(p)) * time.Millisecond }

func secs(p *float64) time.Duration { return time.Duration(num(p) * float64(time.Second)) }

// unixMs reads a Unix-milliseconds timestamp. nil is the zero time.
func unixMs(p *int64) time.Time {
	if p == nil {
		return time.Time{}
	}
	return time.UnixMilli(*p)
}
