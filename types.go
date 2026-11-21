package rendobar

import (
	"encoding/json"
	"fmt"
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
	// JSONSchema is the full JSON Schema of the params, decoded from JSON.
	JSONSchema any `json:"jsonSchema"`
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
	Type        string         `json:"type"`
	Description string         `json:"description"`
	Required    bool           `json:"required"`
	Nullable    bool           `json:"nullable"`
	Default     any            `json:"default"`
	Example     any            `json:"example"`
	Options     []SchemaOption `json:"options"`   // the allowed values when Type is options
	Min         *float64       `json:"min"`       // smallest number, or nil
	Max         *float64       `json:"max"`       // largest number, or nil
	MinLength   *float64       `json:"minLength"` // shortest string, or nil
	MaxLength   *float64       `json:"maxLength"` // longest string, or nil
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
	Slug string `json:"slug"` // free, pro or studio
	Name string `json:"name"` // display name
}

// Limits are the workspace's plan limits.
type Limits struct {
	ConcurrentJobs        int
	MaxQueuedJobs         int
	APIRequestsPerMinute  int
	MaxJobTimeoutSeconds  int
	MaxInputFileSize      int64 // bytes per input file, uploaded or fetched by URL
	StorageQuota          int64 // bytes
	OutputRetentionDays   int
	MaxUploadSessionFiles int
	TeamMembers           int // members the workspace may hold, pending invitations included

	ComposeMaxPixels         int64 // largest compose frame, width times height
	ComposeMaxFPS            int
	ComposeAnimatedMaxPixels int64 // largest compose frame with animated graphics
	ComposeAnimatedMaxFPS    int
}

type wireLimits struct {
	ConcurrentJobs           float64 `json:"concurrentJobs"`
	MaxQueuedJobs            float64 `json:"maxQueuedJobs"`
	APIRequestsPerMinute     float64 `json:"apiRequestsPerMinute"`
	MaxJobTimeout            float64 `json:"maxJobTimeout"`
	MaxInputFileSize         float64 `json:"maxInputFileSize"`
	StorageQuota             float64 `json:"storageQuota"`
	OutputRetentionDays      float64 `json:"outputRetentionDays"`
	MaxUploadSessionFiles    float64 `json:"maxUploadSessionFiles"`
	TeamMembers              float64 `json:"teamMembers"`
	ComposeMaxPixels         float64 `json:"composeMaxPixels"`
	ComposeMaxFps            float64 `json:"composeMaxFps"`
	ComposeAnimatedMaxPixels float64 `json:"composeAnimatedMaxPixels"`
	ComposeAnimatedMaxFps    float64 `json:"composeAnimatedMaxFps"`
}

// UnmarshalJSON reads the limits, which the API sends as JSON numbers.
func (l *Limits) UnmarshalJSON(b []byte) error {
	var w wireLimits
	if err := json.Unmarshal(b, &w); err != nil {
		return err
	}
	*l = Limits{
		ConcurrentJobs:           int(w.ConcurrentJobs),
		MaxQueuedJobs:            int(w.MaxQueuedJobs),
		APIRequestsPerMinute:     int(w.APIRequestsPerMinute),
		MaxJobTimeoutSeconds:     int(w.MaxJobTimeout),
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
