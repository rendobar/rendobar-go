package rendobar

import (
	"context"
	"net/http"
	"net/url"
)

func jobPath(id string) (string, error) {
	if err := checkID(id); err != nil {
		return "", err
	}
	return "/jobs/" + url.PathEscape(id), nil
}

// Logs returns a job's log entries, oldest first.
func (j *Jobs) Logs(ctx context.Context, id string) ([]*JobLogEntry, error) {
	path, err := jobPath(id)
	if err != nil {
		return nil, err
	}
	return callList[JobLogEntry](ctx, j.client, http.MethodGet, path+"/logs", nil, nil)
}

// Timings returns how long each stage of a job took, from submit to output.
// A stage the job has not reached, or that it skipped, is nil.
func (j *Jobs) Timings(ctx context.Context, id string) (*JobTimings, error) {
	path, err := jobPath(id)
	if err != nil {
		return nil, err
	}
	return call[JobTimings](ctx, j.client, http.MethodGet, path+"/timings", nil, nil)
}

// StatsParams picks the window Jobs.Stats counts. The zero value counts the last 24 hours.
type StatsParams struct {
	// Window is how far back to count, such as StatsWindowWeek. "" is the API's default, 24 hours.
	Window StatsWindow
}

// Stats counts the workspace's jobs over a window: totals by outcome, success
// rate, average duration, spend and activity over time.
func (j *Jobs) Stats(ctx context.Context, p StatsParams) (*JobStats, error) {
	q := url.Values{}
	setString(q, "window", p.Window)
	return call[JobStats](ctx, j.client, http.MethodGet, "/jobs/stats", q, nil)
}

// RetryDeliveries sends a completed job's output again to every destination
// that did not receive it, and reports how many it retried.
func (j *Jobs) RetryDeliveries(ctx context.Context, id string) (*DeliveriesRetried, error) {
	path, err := jobPath(id)
	if err != nil {
		return nil, err
	}
	return call[DeliveriesRetried](ctx, j.client, http.MethodPost, path+"/deliveries/retry", nil, nil)
}

// CreateShare makes a public link to a completed job's output. A job that
// already has one keeps it.
func (j *Jobs) CreateShare(ctx context.Context, id string) (*JobShare, error) {
	path, err := jobPath(id)
	if err != nil {
		return nil, err
	}
	return call[JobShare](ctx, j.client, http.MethodPost, path+"/share", nil, nil)
}

// GetShare returns a job's public link, or nil and no error when the job has none.
func (j *Jobs) GetShare(ctx context.Context, id string) (*JobShare, error) {
	path, err := jobPath(id)
	if err != nil {
		return nil, err
	}
	if err := j.client.needKey(); err != nil {
		return nil, err
	}
	var res struct {
		Data *JobShare `json:"data"`
	}
	if err := j.client.getJSON(ctx, path+"/share", nil, &res); err != nil {
		return nil, err
	}
	return res.Data, nil
}

// DeleteShare removes a job's public link. The output stays.
func (j *Jobs) DeleteShare(ctx context.Context, id string) error {
	path, err := jobPath(id)
	if err != nil {
		return err
	}
	return send(ctx, j.client, http.MethodDelete, path+"/share", nil)
}
