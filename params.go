package rendobar

import (
	"encoding/json"
	"fmt"
	"strings"
)

//go:generate go run ./internal/cmd/genparams -spec fern/openapi.json -out spec_gen.go -webhooks webhooks/events_gen.go

// JobParams are the params of one job type. Each job type the SDK knows has
// its own struct, such as FfmpegParams or ImageUpscaleParams, generated from
// the API's published schema, so a misspelt or missing param fails to
// compile. CustomParams covers a job type this SDK predates.
//
// The job type to submit comes from JobType, so the type and its params can
// never disagree. Validate checks the params locally, and Jobs.Create calls it
// before sending.
type JobParams interface {
	// JobType is the job type these params belong to, such as "ffmpeg".
	JobType() string
	// Validate reports what is wrong with the params as an *Error with code
	// INVALID_ARGUMENT and one Problem each, or nil.
	Validate() error
	isJobParams()
}

// CustomParams submits a job type this SDK has no struct for yet, such as one
// the API added after this release. Fields is the job's params as the API
// documents them (see Jobs.Schema). A job type with a generated struct is
// refused here: use that struct, so its checks apply.
type CustomParams struct {
	Type   string
	Fields map[string]any
}

// JobType returns Type.
func (p CustomParams) JobType() string { return p.Type }

// Validate checks that Type names a job type this SDK has no struct for. The
// API checks Fields.
func (p CustomParams) Validate() error {
	var c check
	switch {
	case !jobTypeName.MatchString(p.Type):
		c.fail("type", "must name a job type, such as compose")
	case typedJobTypes[p.Type]:
		c.fail("type", fmt.Sprintf("has typed params in this SDK: use them instead of CustomParams for %s", p.Type))
	}
	return c.err()
}

func (CustomParams) isJobParams() {}

// MarshalJSON sends Fields.
func (p CustomParams) MarshalJSON() ([]byte, error) {
	if p.Fields == nil {
		return []byte("{}"), nil
	}
	return json.Marshal(p.Fields)
}

// Ptr returns a pointer to v, for the optional numbers and booleans in params:
//
//	rendobar.FfmpegParams{Command: "...", Timeout: rendobar.Ptr[int64](600)}
func Ptr[T any](v T) *T { return &v }

// check collects the problems Validate finds.
type check struct{ problems []Problem }

func (c *check) fail(path, message string) {
	c.problems = append(c.problems, Problem{Path: path, Message: message})
}

func (c *check) err() error {
	if len(c.problems) == 0 {
		return nil
	}
	first := c.problems[0]
	msg := "params are not valid: " + strings.TrimSpace(first.Path+" "+first.Message)
	if n := len(c.problems) - 1; n > 0 {
		msg += fmt.Sprintf(" (and %d more)", n)
	}
	return &Error{Code: CodeInvalidArgument, Message: msg, Problems: c.problems}
}

func join(path, key string) string {
	if path == "" {
		return key
	}
	return path + "." + key
}

// marshalWith encodes v and adds key: value, for the const a union branch sends.
func marshalWith(v any, key, value string) ([]byte, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, err
	}
	quoted, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	m[key] = quoted
	return json.Marshal(m)
}
