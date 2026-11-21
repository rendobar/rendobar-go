package rendobar

import (
	"encoding/json"
	"fmt"
)

// Ffprobe reads the output of a complete ffprobe job as the FfprobeResult the
// API's spec describes. It fails when the job is another type or its data is
// not an ffprobe result.
func (c *JobComplete) Ffprobe() (*FfprobeResult, error) {
	if c.Type != (FfprobeParams{}).JobType() {
		return nil, fmt.Errorf("rendobar: job %s is a %s job, not ffprobe", c.ID, c.Type)
	}
	if c.Output.Data == nil {
		return nil, fmt.Errorf("rendobar: ffprobe job %s has no output data", c.ID)
	}
	out := new(FfprobeResult)
	if err := json.Unmarshal(c.Output.Data, out); err != nil {
		return nil, fmt.Errorf("rendobar: ffprobe job %s has unreadable output data: %w", c.ID, err)
	}
	return out, nil
}
