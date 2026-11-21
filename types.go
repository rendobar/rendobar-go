package rendobar

import (
	"encoding/json"
	"fmt"
	"time"
)

// UnixMillis is a timestamp as the API sends it: milliseconds since the Unix
// epoch. 0 means the API did not send one.
type UnixMillis int64

// Time returns the timestamp, or the zero time.Time for 0.
func (u UnixMillis) Time() time.Time {
	if u == 0 {
		return time.Time{}
	}
	return time.UnixMilli(int64(u))
}

// StepStatus is where a job step is. The API documents these values in prose,
// not as a schema enum, so the list is kept here. New values may appear.
type StepStatus string

// The StepStatus values this SDK knows.
const (
	StepPending  StepStatus = "pending"
	StepRunning  StepStatus = "running"
	StepComplete StepStatus = "complete"
	StepSkipped  StepStatus = "skipped"
	StepError    StepStatus = "error"
)

// BootPhase is how far a runner is in starting up for a job. The API documents
// these values in prose, so the list is kept here. New values may appear.
type BootPhase string

// The BootPhase values this SDK knows.
const (
	BootScheduling   BootPhase = "scheduling"
	BootQueued       BootPhase = "queued"
	BootPullingImage BootPhase = "pulling-image"
	BootBooting      BootPhase = "booting"
	BootRestoring    BootPhase = "restoring"
)

// PlanSlug names a Rendobar plan. The API documents these values in prose, so
// the list is kept here. New plans may appear.
type PlanSlug string

// The plans this SDK knows.
const (
	PlanFree   PlanSlug = "free"
	PlanPro    PlanSlug = "pro"
	PlanStudio PlanSlug = "studio"
)

// JobType is one live job type, from Jobs.Types.
type JobType struct {
	Type         string   `json:"type"`         // the name to submit, such as ffmpeg
	Tag          string   `json:"tag"`          // its category
	Summary      string   `json:"summary"`      // one line on what it does
	AcceptsMedia []string `json:"acceptsMedia"` // the media it reads, such as video or audio
	UseCases     []string `json:"useCases"`     // what it is typically used for
	ChainsWith   []string `json:"chainsWith"`   // job types whose output commonly feeds this one, or the other way round
}

// JobTypeSchema is one job type's params, from Jobs.Schema.
type JobTypeSchema struct {
	Type string `json:"type"`
	// Fields are the params in display order.
	Fields []SchemaField `json:"fields"`
	// Inputs are the media files the job reads.
	Inputs SchemaInputs `json:"inputs"`
	// Discriminator names the field that picks a branch when the params are a
	// union of shapes, or "".
	Discriminator string `json:"discriminator"`
	// JSONSchema is the full JSON Schema of the params.
	JSONSchema json.RawMessage `json:"jsonSchema"`
}

// SchemaInputs describes the input files of a job type.
type SchemaInputs struct {
	Fields []SchemaField `json:"fields"`
	// Variadic reports that any file name may be an input key, as with ffmpeg
	// and ffprobe, whose commands name their inputs.
	Variadic bool `json:"variadic"`
}

// SchemaField describes one param or input of a job type.
type SchemaField struct {
	// Key is unique within the job type. Name repeats when a union gives one
	// param several shapes, so use Key to tell fields apart.
	Key string `json:"key"`
	// Name is the param key to submit the value under.
	Name  string `json:"name"`
	Label string `json:"label"`
	// Type is string, number, boolean, options or json.
	Type        FieldType       `json:"type"`
	Description string          `json:"description"`
	Required    bool            `json:"required"`
	Nullable    bool            `json:"nullable"`
	Default     json.RawMessage `json:"default"`   // the default value as JSON, or nil
	Example     json.RawMessage `json:"example"`   // an example value as JSON, or nil
	Options     []SchemaOption  `json:"options"`   // the allowed values when Type is options
	Min         *float64        `json:"min"`       // smallest number, or nil
	Max         *float64        `json:"max"`       // largest number, or nil
	MinLength   *int64          `json:"minLength"` // shortest string, or nil
	MaxLength   *int64          `json:"maxLength"` // longest string, or nil
	// Advanced marks a field a form can tuck behind an Advanced section.
	Advanced bool `json:"advanced"`
	// ShowWhen is set on a field that belongs to one branch of a union, or nil.
	ShowWhen *ShowWhen `json:"showWhen"`
	// URL reports that an input takes the URL of a file. Inputs only.
	URL bool `json:"url"`
	// Multiple reports that an input takes a list of values. Inputs only.
	Multiple bool `json:"multiple"`
}

// SchemaOption is one allowed value of an options field.
type SchemaOption struct {
	Label string `json:"label"`
	Value string `json:"value"`
}

// ShowWhen ties a field to the branch of a union it belongs to.
type ShowWhen struct {
	Field  string   `json:"field"`  // the discriminator field
	Equals []string `json:"equals"` // the field applies when the discriminator holds one of these
}

// AccountCapabilities is what this API key's workspace may do.
type AccountCapabilities struct {
	Plan     Plan     `json:"plan"`
	Limits   Limits   `json:"limits"`
	Features Features `json:"features"`
}

// Plan is the workspace's plan.
type Plan struct {
	Slug PlanSlug `json:"slug"`
	Name string   `json:"name"` // display name
}

// Limits are the workspace's plan limits.
type Limits struct {
	ConcurrentJobs        int           `json:"concurrentJobs"`
	MaxQueuedJobs         int           `json:"maxQueuedJobs"`
	APIRequestsPerMinute  int           `json:"apiRequestsPerMinute"`
	MaxJobTimeout         time.Duration `json:"maxJobTimeout"`    // longest a job may run; encodes as nanoseconds
	MaxInputFileSize      int64         `json:"maxInputFileSize"` // bytes per input file, uploaded or fetched by URL
	StorageQuota          int64         `json:"storageQuota"`     // bytes
	OutputRetentionDays   int           `json:"outputRetentionDays"`
	MaxUploadSessionFiles int           `json:"maxUploadSessionFiles"`
	TeamMembers           int           `json:"teamMembers"` // members the workspace may hold, pending invitations included

	ComposeMaxPixels         int64 `json:"composeMaxPixels"` // largest compose frame, width times height
	ComposeMaxFPS            int   `json:"composeMaxFps"`
	ComposeAnimatedMaxPixels int64 `json:"composeAnimatedMaxPixels"` // largest compose frame with animated graphics
	ComposeAnimatedMaxFPS    int   `json:"composeAnimatedMaxFps"`
}

type wireLimits struct {
	ConcurrentJobs           int64 `json:"concurrentJobs"`
	MaxQueuedJobs            int64 `json:"maxQueuedJobs"`
	APIRequestsPerMinute     int64 `json:"apiRequestsPerMinute"`
	MaxJobTimeout            int64 `json:"maxJobTimeout"`
	MaxInputFileSize         int64 `json:"maxInputFileSize"`
	StorageQuota             int64 `json:"storageQuota"`
	OutputRetentionDays      int64 `json:"outputRetentionDays"`
	MaxUploadSessionFiles    int64 `json:"maxUploadSessionFiles"`
	TeamMembers              int64 `json:"teamMembers"`
	ComposeMaxPixels         int64 `json:"composeMaxPixels"`
	ComposeMaxFps            int64 `json:"composeMaxFps"`
	ComposeAnimatedMaxPixels int64 `json:"composeAnimatedMaxPixels"`
	ComposeAnimatedMaxFps    int64 `json:"composeAnimatedMaxFps"`
}

// UnmarshalJSON reads the limits, turning the timeout from seconds into a Duration.
func (l *Limits) UnmarshalJSON(b []byte) error {
	var w wireLimits
	if err := json.Unmarshal(b, &w); err != nil {
		return err
	}
	*l = Limits{
		ConcurrentJobs:           int(w.ConcurrentJobs),
		MaxQueuedJobs:            int(w.MaxQueuedJobs),
		APIRequestsPerMinute:     int(w.APIRequestsPerMinute),
		MaxJobTimeout:            time.Duration(w.MaxJobTimeout) * time.Second,
		MaxInputFileSize:         int64(w.MaxInputFileSize),
		StorageQuota:             int64(w.StorageQuota),
		OutputRetentionDays:      int(w.OutputRetentionDays),
		MaxUploadSessionFiles:    int(w.MaxUploadSessionFiles),
		TeamMembers:              int(w.TeamMembers),
		ComposeMaxPixels:         int64(w.ComposeMaxPixels),
		ComposeMaxFPS:            int(w.ComposeMaxFps),
		ComposeAnimatedMaxPixels: int64(w.ComposeAnimatedMaxPixels),
		ComposeAnimatedMaxFPS:    int(w.ComposeAnimatedMaxFps),
	}
	return nil
}

// Features are the features the workspace's plan turns on.
type Features struct {
	RawFFmpeg       bool `json:"rawFfmpeg"`
	MCP             bool `json:"mcp"`
	Webhooks        bool `json:"webhooks"`
	PriorityQueue   bool `json:"priorityQueue"`
	CustomRunners   bool `json:"customRunners"`
	GPU             bool `json:"gpu"`
	PrioritySupport bool `json:"prioritySupport"` // support replies the same business day
	AuditLog        bool `json:"auditLog"`
	// FastOutputDelivery streams outputs of 512 MiB or more into destination
	// buckets while they upload.
	FastOutputDelivery bool `json:"fastOutputDelivery"`
}

// convert re-reads a value the generated core decoded as one of this
// package's types, through its JSON. The public types never expose generated ones.
func convert[T any](in any) (*T, error) {
	raw, err := json.Marshal(in)
	if err != nil {
		return nil, fmt.Errorf("rendobar: unreadable response: %w", err)
	}
	out := new(T)
	if err := json.Unmarshal(raw, out); err != nil {
		return nil, fmt.Errorf("rendobar: unreadable response: %w", err)
	}
	return out, nil
}
