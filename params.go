package rendobar

import (
	"encoding/json"
	"fmt"
	"strings"
)

//go:generate go run ./internal/cmd/genparams -spec fern/openapi.json -out spec_gen.go -webhooks webhooks/events_gen.go

// JobParams are the params of one job type. Each job type the SDK knows has
// its own struct, such as FfmpegParams or ImageUpscaleParams, generated from
// the API's published schema, so a misspelt param or a wrong value type fails
// to compile. CustomParams is the one unchecked path, for anything the typed
// structs cannot say yet.
//
// The job type to submit comes from JobType, so the type and its params can
// never disagree. Validate checks the params locally, and Jobs.Create calls it
// before sending.
type JobParams interface {
	// JobType is the job type these params belong to, such as JobTypeFfmpeg.
	JobType() JobType
	// Validate reports what is wrong with the params as an *Error with code
	// INVALID_ARGUMENT and one Problem each, or nil.
	Validate() error
	isJobParams()
}

// CustomParams is the one unchecked way to submit a job: any job type, with
// its params as the API documents them (see Jobs.Schema). Use it for a job
// type this SDK predates, such as compose, or for a param or value the API
// added to a typed job after this release. The SDK checks only that Type is
// a job type name. The API checks Fields. Prefer the typed struct when it can
// say what you need, so its checks apply before sending.
type CustomParams struct {
	Type   JobType
	Fields map[string]any
}

// JobType returns Type.
func (p CustomParams) JobType() JobType { return p.Type }

// Validate checks that Type is a job type name. The API checks Fields.
func (p CustomParams) Validate() error {
	var c check
	if !jobTypeName.MatchString(string(p.Type)) {
		c.fail("type", "must name a job type, such as compose")
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
