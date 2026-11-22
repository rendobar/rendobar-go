package realtime

import (
	"encoding/json"
	"strings"

	"github.com/rendobar/rendobar-go"
)

// Event is one message from the stream: a pointer to one of the types in this
// file, such as *JobProgress or *BalanceUpdated, or *Unknown for a message
// this SDK cannot read. Read it with a type switch that keeps a default branch.
type Event interface {
	// Type is the event's name on the wire, such as "job.progress".
	Type() string
	// EventID is the stream's cursor for the event, or 0 for an event the
	// workspace does not keep for replay, such as progress. Pass the last one
	// you handled as EventsParams.LastEventID to resume after it.
	EventID() int64
	isEvent()
}

// cursor carries an event's EventID.
type cursor struct{ id int64 }

// EventID returns the event's replay cursor, or 0.
func (c cursor) EventID() int64 { return c.id }

func (cursor) isEvent() {}

func (c *cursor) set(v cursor) { *c = v }

// Connected is yielded each time the stream connects, the first time included.
// Events the workspace kept since LastEventID follow, then ReplayDone.
type Connected struct{ cursor }

// ReplayDone says the replayed events are over: what follows is live.
type ReplayDone struct{ cursor }

// ResyncReason says why the stream asks for a resync.
type ResyncReason string

// The ResyncReason values this SDK knows. The API may add values.
const (
	ResyncBufferOverflow ResyncReason = "buffer_overflow"
	ResyncServerRestart  ResyncReason = "server_restart"
)

// Resync says the stream lost events it cannot replay. Read the state you
// show again, such as Jobs.List.
type Resync struct {
	cursor
	Reason ResyncReason `json:"reason"`
}

// Unknown is a message this SDK cannot read: a type newer than it, or a known
// type whose fields changed shape. Raw is the message as sent.
type Unknown struct {
	cursor
	Name string
	Raw  json.RawMessage
}

// JobCreated is a job the workspace submitted.
type JobCreated struct {
	cursor
	JobID   string             `json:"jobId"`
	JobType rendobar.JobType   `json:"jobType"`
	Status  rendobar.JobStatus `json:"status"`
}

// JobStatusChanged is a job moving to another status.
type JobStatusChanged struct {
	cursor
	JobID  string             `json:"jobId"`
	Status rendobar.JobStatus `json:"status"`
	// Error is the failure message, for a failed job.
	Error string `json:"error"`
	// Price is what the job cost in nanodollars, once it ended, or nil.
	Price          *int64 `json:"price"`
	PriceFormatted string `json:"priceFormatted"`
}

// Final reports whether the status is one a job ends in: complete, failed or cancelled.
func (e *JobStatusChanged) Final() bool {
	switch e.Status {
	case rendobar.StatusComplete, rendobar.StatusFailed, rendobar.StatusCancelled:
		return true
	}
	return false
}

// JobResult says a job's output is ready. Jobs.Get reads it.
type JobResult struct {
	cursor
	JobID string `json:"jobId"`
}

// JobRegionFailover is a job moved to another region to run.
type JobRegionFailover struct {
	cursor
	JobID      string              `json:"jobId"`
	FromRegion string              `json:"fromRegion"`
	ToRegion   string              `json:"toRegion"`
	Timestamp  rendobar.UnixMillis `json:"timestamp"`
}

// Phase is where a job is, for a progress display. The API may add phases.
type Phase string

// The Phase values this SDK knows.
const (
	PhasePreparing  Phase = "preparing"
	PhaseProcessing Phase = "processing"
	PhaseFinalizing Phase = "finalizing"
)

// JobProgress is how far a running job is. Build a progress display on
// Phase and Progress. Step is the internal step, which varies by job type.
type JobProgress struct {
	cursor
	JobID     string              `json:"jobId"`
	Timestamp rendobar.UnixMillis `json:"timestamp"`
	// Progress is the fraction done, 0 to 1, or nil when it cannot be told.
	Progress *float64 `json:"progress"`
	// Phase is "" from a runner that predates phases.
	Phase        Phase    `json:"phase"`
	Step         string   `json:"step"`
	StepProgress *float64 `json:"stepProgress"`
	// ETA is the seconds left, or nil. It may grow. Progress does not shrink.
	ETA     *float64          `json:"eta"`
	Metrics map[string]Metric `json:"metrics"`
}

// Metric is one value a job reports with its progress: a number or a text.
type Metric struct {
	Number *float64 // set for a number
	Text   string   // set for a text
}

// UnmarshalJSON reads a number or a string.
func (m *Metric) UnmarshalJSON(b []byte) error {
	if len(b) > 0 && b[0] == '"' {
		return json.Unmarshal(b, &m.Text)
	}
	m.Number = new(float64)
	return json.Unmarshal(b, m.Number)
}

// JobStep is a job's internal step starting or ending. It varies by job type,
// so prefer JobProgress for a display.
type JobStep struct {
	cursor
	JobID      string              `json:"jobId"`
	Timestamp  rendobar.UnixMillis `json:"timestamp"`
	Step       string              `json:"step"`
	StepName   string              `json:"stepName"`
	Status     rendobar.StepStatus `json:"status"`
	Index      int64               `json:"index"`
	Total      int64               `json:"total"`
	DurationMs *float64            `json:"durationMs"`
}

// LogLevel is a log entry's level. The API may add levels.
type LogLevel string

// The LogLevel values this SDK knows.
const (
	LogDebug LogLevel = "debug"
	LogInfo  LogLevel = "info"
	LogWarn  LogLevel = "warn"
	LogError LogLevel = "error"
)

// JobLog is one log line of a running job. Jobs.Logs reads them all later.
type JobLog struct {
	cursor
	JobID      string                     `json:"jobId"`
	Timestamp  rendobar.UnixMillis        `json:"timestamp"`
	Level      LogLevel                   `json:"level"`
	Message    string                     `json:"message"`
	Step       string                     `json:"step"`
	Event      string                     `json:"event"`
	DurationMs *float64                   `json:"durationMs"`
	Meta       map[string]json.RawMessage `json:"meta"` // free-form detail, as JSON
}

// JobContext is the machine a job runs on.
type JobContext struct {
	cursor
	JobID     string              `json:"jobId"`
	Timestamp rendobar.UnixMillis `json:"timestamp"`
	Machine   string              `json:"machine"`
	CPU       *float64            `json:"cpu"`
	Memory    *float64            `json:"memory"`
	Region    string              `json:"region"`
}

// JobResources is one sample of what a running job uses. A value the runner
// could not measure is nil.
type JobResources struct {
	cursor
	JobID           string              `json:"jobId"`
	Timestamp       rendobar.UnixMillis `json:"timestamp"`
	CPUPct          *float64            `json:"cpuPct"`
	CPUCores        float64             `json:"cpuCores"`
	AllocatedCores  *float64            `json:"allocatedCores"`
	MemUsedMB       float64             `json:"memUsedMB"`
	MemLimitMB      *float64            `json:"memLimitMB"`
	Threads         *float64            `json:"threads"`
	CPUThrottledPct *float64            `json:"cpuThrottledPct"`
	CPUPressurePct  *float64            `json:"cpuPressurePct"`
	MemPressurePct  *float64            `json:"memPressurePct"`
	IOPressurePct   *float64            `json:"ioPressurePct"`
	DiskUsedMB      *float64            `json:"diskUsedMB"`
	DiskRMBps       *float64            `json:"diskRMBps"`
	DiskWMBps       *float64            `json:"diskWMBps"`
	NetRxMBps       *float64            `json:"netRxMBps"`
	NetTxMBps       *float64            `json:"netTxMBps"`
	GPUPct          *float64            `json:"gpuPct"`
	GPUMemUsedMB    *float64            `json:"gpuMemUsedMB"`
	GPUMemTotalMB   *float64            `json:"gpuMemTotalMB"`
	GPUTempC        *float64            `json:"gpuTempC"`
	GPUPowerW       *float64            `json:"gpuPowerW"`
	GPUEncPct       *float64            `json:"gpuEncPct"`
	GPUDecPct       *float64            `json:"gpuDecPct"`
	GPUMemBwPct     *float64            `json:"gpuMemBwPct"`
}

// JobPreview is a small frame of an image while it generates. Frames are not
// replayed, and one with a Seq at or below the last you showed is stale.
type JobPreview struct {
	cursor
	JobID     string              `json:"jobId"`
	Timestamp rendobar.UnixMillis `json:"timestamp"`
	Seq       int64               `json:"seq"`
	Progress  *float64            `json:"progress"` // 0 to 1, or nil
	Width     int64               `json:"width"`
	Height    int64               `json:"height"`
	Data      string              `json:"data"` // a base64 WebP image, about 256 px on its long edge
}

// BalanceUpdated is the workspace's balance changing.
type BalanceUpdated struct {
	cursor
	Balance          int64               `json:"balance"` // nanodollars
	TxnType          string              `json:"txnType"`
	TxnAmount        *int64              `json:"txnAmount"` // nanodollars
	BalanceFormatted string              `json:"balanceFormatted"`
	Timestamp        rendobar.UnixMillis `json:"timestamp"`
}

// SubscriptionUpdated is the workspace's plan subscription changing.
type SubscriptionUpdated struct {
	cursor
	PlanID            string              `json:"planId"`
	Status            string              `json:"status"`
	CancelAtPeriodEnd *bool               `json:"cancelAtPeriodEnd"`
	Timestamp         rendobar.UnixMillis `json:"timestamp"`
}

// APIKeysUpdated says a key was created, changed or revoked. It carries no
// key: read APIKeys.List again.
type APIKeysUpdated struct {
	cursor
	Timestamp rendobar.UnixMillis `json:"timestamp"`
}

// StorageConnected is a storage connection that finished connecting, such as an AWS stack reporting in.
type StorageConnected struct {
	cursor
	ID        string              `json:"id"`
	Timestamp rendobar.UnixMillis `json:"timestamp"`
}

// StorageRemoved is a storage connection that went away, such as its AWS stack being deleted.
type StorageRemoved struct {
	cursor
	ID        string              `json:"id"`
	Timestamp rendobar.UnixMillis `json:"timestamp"`
}

// StorageProblem says a storage connection failed. Storage.Get says why.
type StorageProblem struct {
	cursor
	ID        string              `json:"id"`
	Timestamp rendobar.UnixMillis `json:"timestamp"`
}

// Notification is a notification for the workspace.
type Notification struct {
	cursor
	ID          string                        `json:"id"`
	Severity    rendobar.NotificationSeverity `json:"severity"`
	Title       string                        `json:"title"`
	Message     string                        `json:"message"`
	Href        string                        `json:"href"` // a dashboard path it is about, or ""
	RequiresAck bool                          `json:"requiresAck"`
}

// OrgUpdated is the workspace renamed or given another slug.
type OrgUpdated struct {
	cursor
	Name      string              `json:"name"`
	Slug      string              `json:"slug"`
	Timestamp rendobar.UnixMillis `json:"timestamp"`
}

// LeaveReason says why someone is no longer a member. The API may add reasons.
type LeaveReason string

// The LeaveReason values this SDK knows.
const (
	LeaveRemoved LeaveReason = "removed" // another member removed them
	LeaveSelf    LeaveReason = "left"    // they left on their own
)

// MemberUpdated is a member's role changing, or a member leaving.
type MemberUpdated struct {
	cursor
	UserID string `json:"userId"`
	// Role is the new role, or nil when they are no longer a member.
	Role *rendobar.MemberRole `json:"role"`
	// Reason says why Role is nil, or "".
	Reason    LeaveReason         `json:"reason"`
	Timestamp rendobar.UnixMillis `json:"timestamp"`
}

func (*Connected) Type() string           { return "connected" }
func (*ReplayDone) Type() string          { return "replay.done" }
func (*Resync) Type() string              { return "resync" }
func (e *Unknown) Type() string           { return e.Name }
func (*JobCreated) Type() string          { return "job.created" }
func (*JobStatusChanged) Type() string    { return "job.status" }
func (*JobResult) Type() string           { return "job.result" }
func (*JobRegionFailover) Type() string   { return "job.region_failover" }
func (*JobProgress) Type() string         { return "job.progress" }
func (*JobStep) Type() string             { return "job.step" }
func (*JobLog) Type() string              { return "job.log" }
func (*JobContext) Type() string          { return "job.context" }
func (*JobResources) Type() string        { return "job.resources" }
func (*JobPreview) Type() string          { return "job.preview" }
func (*BalanceUpdated) Type() string      { return "balance.updated" }
func (*SubscriptionUpdated) Type() string { return "subscription.updated" }
func (*APIKeysUpdated) Type() string      { return "apikeys.updated" }
func (*StorageConnected) Type() string    { return "storage.connected" }
func (*StorageRemoved) Type() string      { return "storage.removed" }
func (*StorageProblem) Type() string      { return "storage.problem" }
func (*Notification) Type() string        { return "notification" }
func (*OrgUpdated) Type() string          { return "org.updated" }
func (*MemberUpdated) Type() string       { return "member.updated" }

// decoders maps a wire name to a new event of its type. "job.metrics" is the
// old name of "job.resources".
var decoders = map[string]func() Event{
	"replay.done":          func() Event { return new(ReplayDone) },
	"resync":               func() Event { return new(Resync) },
	"job.created":          func() Event { return new(JobCreated) },
	"job.status":           func() Event { return new(JobStatusChanged) },
	"job.result":           func() Event { return new(JobResult) },
	"job.region_failover":  func() Event { return new(JobRegionFailover) },
	"job.progress":         func() Event { return new(JobProgress) },
	"job.step":             func() Event { return new(JobStep) },
	"job.log":              func() Event { return new(JobLog) },
	"job.context":          func() Event { return new(JobContext) },
	"job.resources":        func() Event { return new(JobResources) },
	"job.metrics":          func() Event { return new(JobResources) },
	"job.preview":          func() Event { return new(JobPreview) },
	"balance.updated":      func() Event { return new(BalanceUpdated) },
	"subscription.updated": func() Event { return new(SubscriptionUpdated) },
	"apikeys.updated":      func() Event { return new(APIKeysUpdated) },
	"storage.connected":    func() Event { return new(StorageConnected) },
	"storage.removed":      func() Event { return new(StorageRemoved) },
	"storage.problem":      func() Event { return new(StorageProblem) },
	"notification":         func() Event { return new(Notification) },
	"org.updated":          func() Event { return new(OrgUpdated) },
	"member.updated":       func() Event { return new(MemberUpdated) },
}

// required are the fields each type must carry to be read as that type, so a
// message whose shape changed reads as *Unknown rather than a half-empty event.
var required = map[string][]string{
	"job.created": {"jobId", "jobType"}, "job.status": {"jobId", "status"}, "job.result": {"jobId"},
	"job.region_failover": {"jobId", "fromRegion", "toRegion"}, "job.progress": {"jobId", "step"},
	"job.step": {"jobId", "step", "status"}, "job.log": {"jobId", "level", "message"},
	"job.context": {"jobId", "machine"}, "job.resources": {"jobId", "timestamp"}, "job.metrics": {"jobId", "timestamp"},
	"job.preview": {"jobId", "seq", "data"}, "balance.updated": {"balance"}, "subscription.updated": {"planId", "status"},
	"storage.connected": {"id"}, "storage.removed": {"id"}, "storage.problem": {"id"},
	"notification": {"severity", "title", "message"}, "org.updated": {"name", "slug"},
	"member.updated": {"userId", "role"}, "resync": {"reason"},
}

// decode reads one frame. A frame that is not a JSON object with a string
// type returns ok false and is dropped.
func decode(raw []byte) (ev Event, id int64, ok bool) {
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		return nil, 0, false
	}
	var name string
	if json.Unmarshal(fields["type"], &name) != nil || name == "" {
		return nil, 0, false
	}
	// The replay cursor rides in eventId. An older hub wrote it into a numeric
	// id, which an event's own id (a string) never is.
	if json.Unmarshal(fields["eventId"], &id) != nil || id == 0 {
		id = 0
		if v, present := fields["id"]; present && !strings.HasPrefix(strings.TrimSpace(string(v)), `"`) {
			_ = json.Unmarshal(v, &id)
			delete(fields, "id")
		}
	}
	delete(fields, "eventId")
	c := cursor{id}
	newEvent, known := decoders[name]
	if !known || !has(fields, required[name]) {
		return &Unknown{cursor: c, Name: name, Raw: raw}, id, true
	}
	ev = newEvent()
	body, err := json.Marshal(fields)
	if err != nil || json.Unmarshal(body, ev) != nil {
		return &Unknown{cursor: c, Name: name, Raw: raw}, id, true
	}
	setCursor(ev, c)
	return ev, id, true
}

func has(fields map[string]json.RawMessage, names []string) bool {
	for _, n := range names {
		if _, ok := fields[n]; !ok {
			return false
		}
	}
	return true
}

// setCursor stores the cursor in the event's embedded cursor field.
func setCursor(ev Event, c cursor) {
	if s, ok := ev.(interface{ set(cursor) }); ok {
		s.set(c)
	}
}

// jobEvent is an event about one job.
type jobEvent interface{ jobID() string }

func (e *JobCreated) jobID() string        { return e.JobID }
func (e *JobStatusChanged) jobID() string  { return e.JobID }
func (e *JobResult) jobID() string         { return e.JobID }
func (e *JobRegionFailover) jobID() string { return e.JobID }
func (e *JobProgress) jobID() string       { return e.JobID }
func (e *JobStep) jobID() string           { return e.JobID }
func (e *JobLog) jobID() string            { return e.JobID }
func (e *JobContext) jobID() string        { return e.JobID }
func (e *JobResources) jobID() string      { return e.JobID }
func (e *JobPreview) jobID() string        { return e.JobID }

func (e *Unknown) jobID() string {
	var f struct {
		JobID string `json:"jobId"`
	}
	_ = json.Unmarshal(e.Raw, &f)
	return f.JobID
}

// aboutJob reports whether ev concerns the job id. A job's stream also
// carries the workspace's other events, which Client.Job leaves out.
func aboutJob(ev Event, id string) bool {
	if _, ok := ev.(*Connected); ok {
		return true
	}
	j, ok := ev.(jobEvent)
	return ok && j.jobID() == id
}
