package rendobar

import (
	"fmt"
	"time"

	"github.com/rendobar/rendobar-go/internal/api"
)

// JobStatus is where a job is in its life. A job moves from waiting to
// dispatched to running, and ends complete, failed or cancelled. The API may
// add statuses later, so treat an unknown one as still in progress.
type JobStatus string

// The job statuses this SDK knows.
const (
	StatusWaiting    JobStatus = "waiting"    // queued, not yet handed to a runner
	StatusDispatched JobStatus = "dispatched" // handed to a runner that has not started
	StatusRunning    JobStatus = "running"    // in progress
	StatusComplete   JobStatus = "complete"   // finished with its output
	StatusFailed     JobStatus = "failed"     // ended with an error
	StatusCancelled  JobStatus = "cancelled"  // cancelled before it finished
)

func (s JobStatus) terminal() bool {
	return s == StatusComplete || s == StatusFailed || s == StatusCancelled
}

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

	status JobStatus
	info   JobInfo
}

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

// JobInfo holds what a job carries in every status. Each status type embeds
// it, so job.Complete.ID and job.Failed.CreatedAt read it directly. A time
// the job has not reached yet is the zero time.Time.
type JobInfo struct {
	ID    string // job id, such as job_abc123
	OrgID string // the workspace that owns the job
	Type  string // job type, such as ffmpeg
	Label string // the name given at submit, or ""

	Source         string         // how the job was created: api, dashboard, mcp and others
	Client         string         // the client that sent it, such as sdk, cli or n8n, or ""
	IdempotencyKey string         // the key the submit carried, or ""
	Params         map[string]any // the params as submitted
	Inputs         map[string]any // the input files as submitted

	// MediaType is the detected media of the input: video, image or audio, or "".
	MediaType string
	// OutputCategory is the kind of output the job makes: video, image,
	// audio, json, captions or raw. New values may appear.
	OutputCategory string
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
	// CallbackStatus is the delivery state of the callback set at submit:
	// pending, delivered or failed. "" when the job has no callback.
	CallbackStatus string
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
	// Data is the job type's computed result, such as an ffprobe report or a
	// transcript, decoded from JSON. nil when the job produced no data.
	Data any
	// File is the single headline file: the one output file, or the stream
	// manifest (.m3u8 or .mpd). nil for data jobs and for plain sets of files.
	File *OutputFile
	// Files is every file the job produced. Empty for data jobs.
	Files []OutputFile
	// ExpiresAt is when the file URLs stop working. Zero when there are no files.
	ExpiresAt time.Time
}

// OutputFile is one file a job produced.
type OutputFile struct {
	URL  string // a ready-to-fetch URL that expires at JobOutput.ExpiresAt
	Path string // the path within the job output, such as the name an ffmpeg command wrote
	// Type is the file type from its extension: video, image, audio,
	// captions, playlist, data or other. New values may appear.
	Type string
	Size int64 // bytes
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
	Code      string // stable machine-readable code, such as INPUT_UNREADABLE
	Message   string // human-readable explanation
	Detail    string // the tail of the tool's stderr, such as ffmpeg's, or ""
	Retryable bool   // true when running the same job again may succeed
	// Phase is the phase the job failed in: preparing, processing or
	// finalizing, or "" when unknown.
	Phase string
}

// Cost is what a job was charged.
type Cost struct {
	Nanodollars int64  // the amount, where 1 USD is 1,000,000,000
	Currency    string // ISO 4217 code, such as USD
	Formatted   string // the amount for display, such as $0.50
}

// JobStep is one execution step of a job.
type JobStep struct {
	ID     string
	Name   string
	Status string // pending, running, complete, skipped or error
	Error  string // why the step failed, or ""
	Meta   map[string]any

	StartedAt   time.Time
	CompletedAt time.Time
	Duration    time.Duration
}

// Delivery is one destination a job's output goes to.
type Delivery struct {
	StorageID string // the storage connection
	Status    string // pending, delivered or failed
	// Path is the object key in the bucket. A folder of files ends in /.
	Path string
	// URL is where to fetch the delivered file, or "" for a folder.
	URL string
	// Reason is why a delivery failed, such as destination_denied or
	// already_exists, or "".
	Reason string
	// Renamed reports that the name was taken, so the job id was appended.
	Renamed bool
}

// BootState describes a runner starting up for a dispatched job.
type BootState struct {
	// Phase is scheduling, queued, pulling-image, booting or restoring.
	Phase string
	// Detail is a human-readable line, such as "waiting for GPU capacity".
	Detail  string
	Elapsed time.Duration // since dispatch
	Typical time.Duration // a typical start-up for this workload, or 0 when unknown
	ETA     time.Duration // estimated time left for the whole job, or 0 when unknown
}

// The wire shape of a job. The generated core decodes the response, and
// fromAPIJob re-reads its JSON into this, then builds the public types.
type wireJob struct {
	ID                 string         `json:"id"`
	OrgID              string         `json:"orgId"`
	Type               string         `json:"type"`
	Status             string         `json:"status"`
	Label              *string        `json:"label"`
	Source             string         `json:"source"`
	Client             *string        `json:"client"`
	IdempotencyKey     *string        `json:"idempotencyKey"`
	Params             map[string]any `json:"params"`
	Inputs             map[string]any `json:"inputs"`
	MediaType          *string        `json:"mediaType"`
	OutputCategory     string         `json:"outputCategory"`
	Model              *string        `json:"model"`
	Region             *string        `json:"region"`
	TimeoutMs          *float64       `json:"timeoutMs"`
	Cost               *wireCost      `json:"cost"`
	RetryCount         int            `json:"retryCount"`
	Steps              []wireStep     `json:"steps"`
	LogsAvailable      bool           `json:"logsAvailable"`
	ResourcesAvailable bool           `json:"resourcesAvailable"`
	Callback           *struct {
		Status string `json:"status"`
	} `json:"callback"`
	Deliveries         []wireDelivery `json:"deliveries"`
	WebURL             string         `json:"webUrl"`
	CreatedAt          *float64       `json:"createdAt"`
	DispatchedAt       *float64       `json:"dispatchedAt"`
	StartedAt          *float64       `json:"startedAt"`
	CompletedAt        *float64       `json:"completedAt"`
	SettledAt          *float64       `json:"settledAt"`
	RetentionExpiresAt *float64       `json:"retentionExpiresAt"`

	Progress  *float64       `json:"progress"`
	Eta       *float64       `json:"eta"`
	BootState *wireBootState `json:"bootState"`
	Output    *wireOutput    `json:"output"`
	Error     *wireJobError  `json:"error"`
}

type wireCost struct {
	Amount    float64 `json:"amount"`
	Currency  string  `json:"currency"`
	Formatted string  `json:"formatted"`
}

type wireStep struct {
	ID          string         `json:"id"`
	Name        string         `json:"name"`
	Status      string         `json:"status"`
	Error       *string        `json:"error"`
	Meta        map[string]any `json:"meta"`
	StartedAt   *float64       `json:"startedAt"`
	CompletedAt *float64       `json:"completedAt"`
	DurationMs  *float64       `json:"durationMs"`
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
	Phase     string   `json:"phase"`
	Detail    string   `json:"detail"`
	ElapsedMs float64  `json:"elapsedMs"`
	TypicalMs *float64 `json:"typicalMs"`
	EtaMs     *float64 `json:"etaMs"`
}

type wireOutput struct {
	Data      any        `json:"data"`
	File      *wireFile  `json:"file"`
	Files     []wireFile `json:"files"`
	ExpiresAt *float64   `json:"expiresAt"`
}

type wireFile struct {
	URL    string  `json:"url"`
	Path   string  `json:"path"`
	Type   string  `json:"type"`
	Size   float64 `json:"size"`
	Sha256 *string `json:"sha256"`
	Meta   *struct {
		Format     *string  `json:"format"`
		Width      *float64 `json:"width"`
		Height     *float64 `json:"height"`
		DurationMs *float64 `json:"durationMs"`
		Seed       *float64 `json:"seed"`
	} `json:"meta"`
}

type wireJobError struct {
	Code        string  `json:"code"`
	Message     string  `json:"message"`
	Detail      *string `json:"detail"`
	Retryable   bool    `json:"retryable"`
	FailedPhase *string `json:"failedPhase"`
}

// fromAPIJob turns the generated core's job into the public Job.
func fromAPIJob(in *api.Job) (*Job, error) {
	if in == nil {
		return nil, fmt.Errorf("rendobar: response has no job")
	}
	w, err := convert[wireJob](in)
	if err != nil {
		return nil, err
	}
	return w.job(), nil
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
		Params:             w.Params,
		Inputs:             w.Inputs,
		MediaType:          str(w.MediaType),
		OutputCategory:     w.OutputCategory,
		Model:              str(w.Model),
		Region:             str(w.Region),
		Timeout:            ms(w.TimeoutMs),
		RetryCount:         w.RetryCount,
		LogsAvailable:      w.LogsAvailable,
		ResourcesAvailable: w.ResourcesAvailable,
		WebURL:             w.WebURL,
		CreatedAt:          unixMs(w.CreatedAt),
		DispatchedAt:       unixMs(w.DispatchedAt),
		StartedAt:          unixMs(w.StartedAt),
		CompletedAt:        unixMs(w.CompletedAt),
		SettledAt:          unixMs(w.SettledAt),
		RetentionExpiresAt: unixMs(w.RetentionExpiresAt),
	}
	if w.Cost != nil {
		info.Cost = &Cost{Nanodollars: int64(w.Cost.Amount), Currency: w.Cost.Currency, Formatted: w.Cost.Formatted}
	}
	if w.Callback != nil {
		info.CallbackStatus = w.Callback.Status
	}
	for _, s := range w.Steps {
		info.Steps = append(info.Steps, JobStep{
			ID: s.ID, Name: s.Name, Status: s.Status, Error: str(s.Error), Meta: s.Meta,
			StartedAt: unixMs(s.StartedAt), CompletedAt: unixMs(s.CompletedAt), Duration: ms(s.DurationMs),
		})
	}
	for _, d := range w.Deliveries {
		info.Deliveries = append(info.Deliveries, Delivery{
			StorageID: d.StorageID, Status: d.Status, Path: str(d.Path), URL: str(d.URL), Reason: str(d.Reason),
			Renamed: d.Renamed != nil && *d.Renamed,
		})
	}

	job := &Job{status: JobStatus(w.Status), info: info}
	switch job.status {
	case StatusWaiting:
		job.Waiting = &JobWaiting{JobInfo: info}
	case StatusDispatched:
		job.Dispatched = &JobDispatched{JobInfo: info, Progress: w.Progress, ETA: secs(w.Eta)}
		if b := w.BootState; b != nil {
			job.Dispatched.Boot = &BootState{
				Phase: b.Phase, Detail: b.Detail,
				Elapsed: ms(&b.ElapsedMs), Typical: ms(b.TypicalMs), ETA: ms(b.EtaMs),
			}
		}
	case StatusRunning:
		job.Running = &JobRunning{JobInfo: info, Progress: w.Progress, ETA: secs(w.Eta)}
	case StatusComplete:
		job.Complete = &JobComplete{JobInfo: info}
		if o := w.Output; o != nil {
			out := JobOutput{Data: o.Data, ExpiresAt: unixMs(o.ExpiresAt)}
			if o.File != nil {
				f := o.File.file()
				out.File = &f
			}
			for _, f := range o.Files {
				out.Files = append(out.Files, f.file())
			}
			job.Complete.Output = out
		}
	case StatusFailed:
		job.Failed = &JobFailed{JobInfo: info}
		if e := w.Error; e != nil {
			job.Failed.Error = JobError{
				Code: e.Code, Message: e.Message, Detail: str(e.Detail), Retryable: e.Retryable, Phase: str(e.FailedPhase),
			}
		}
	case StatusCancelled:
		job.Cancelled = &JobCancelled{JobInfo: info}
	}
	return job
}

func (f wireFile) file() OutputFile {
	out := OutputFile{URL: f.URL, Path: f.Path, Type: f.Type, Size: int64(f.Size), SHA256: str(f.Sha256)}
	if m := f.Meta; m != nil {
		out.Format = str(m.Format)
		out.Width = int(num(m.Width))
		out.Height = int(num(m.Height))
		out.Duration = ms(m.DurationMs)
		out.Seed = int64(num(m.Seed))
	}
	return out
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

func ms(p *float64) time.Duration { return time.Duration(num(p) * float64(time.Millisecond)) }

func secs(p *float64) time.Duration { return time.Duration(num(p) * float64(time.Second)) }

// unixMs reads a Unix-milliseconds timestamp. nil is the zero time.
func unixMs(p *float64) time.Time {
	if p == nil {
		return time.Time{}
	}
	return time.UnixMilli(int64(*p))
}
