package webhooks

import (
	"encoding/json"
	"time"

	"github.com/rendobar/rendobar-go"
)

// JobEvent is the data of job.created, job.started, job.completed,
// job.failed and job.cancelled. Each event fills what it carries: Output and
// Cost on job.completed, Error on job.failed. A time the job has not reached
// is the zero time.Time. Call Jobs.Get for the full job.
type JobEvent struct {
	JobID   string
	JobType string
	Status  rendobar.JobStatus

	Output         *rendobar.JobOutput // job.completed
	Cost           *rendobar.Cost      // job.completed, once billed
	Error          *rendobar.JobError  // job.failed
	Steps          []rendobar.JobStep
	OutputCategory rendobar.OutputCategory
	MediaType      string

	CreatedAt    time.Time
	DispatchedAt time.Time
	StartedAt    time.Time
	CompletedAt  time.Time
	FailedAt     time.Time
	CancelledAt  time.Time
}

// Job reads the data of a job lifecycle event. It fails for any other event.
func (e *Event) Job() (*JobEvent, error) {
	var w struct {
		JobID          string              `json:"jobId"`
		JobType        string              `json:"jobType"`
		Status         string              `json:"status"`
		Output         *rendobar.JobOutput `json:"output"`
		Cost           *rendobar.Cost      `json:"cost"`
		Error          *rendobar.JobError  `json:"error"`
		Steps          []rendobar.JobStep  `json:"steps"`
		OutputCategory string              `json:"outputCategory"`
		MediaType      *string             `json:"mediaType"`
		Timing         struct {
			CreatedAt, DispatchedAt, StartedAt, CompletedAt, FailedAt, CancelledAt *float64
		} `json:"timing"`
	}
	if err := e.decode(&w, JobCreated, JobStarted, JobCompleted, JobFailed, JobCancelled); err != nil {
		return nil, err
	}
	out := &JobEvent{
		JobID: w.JobID, JobType: w.JobType, Status: rendobar.JobStatus(w.Status),
		Output: w.Output, Cost: w.Cost, Error: w.Error, Steps: w.Steps,
		OutputCategory: rendobar.OutputCategory(w.OutputCategory),
		CreatedAt:      unixMs(w.Timing.CreatedAt), DispatchedAt: unixMs(w.Timing.DispatchedAt),
		StartedAt: unixMs(w.Timing.StartedAt), CompletedAt: unixMs(w.Timing.CompletedAt),
		FailedAt: unixMs(w.Timing.FailedAt), CancelledAt: unixMs(w.Timing.CancelledAt),
	}
	if w.MediaType != nil {
		out.MediaType = *w.MediaType
	}
	return out, nil
}

// DeliveryEvent is the data of job.delivery_succeeded, job.delivery_failed
// and job.deliveries_settled.
type DeliveryEvent struct {
	JobID string
	// Delivery is the one destination a delivery_succeeded or delivery_failed
	// event reports, else nil.
	Delivery *rendobar.Delivery
	// Deliveries is every destination, each delivered or failed, on
	// job.deliveries_settled.
	Deliveries []rendobar.Delivery
}

// Delivery reads the data of a delivery event. It fails for any other event.
func (e *Event) Delivery() (*DeliveryEvent, error) {
	var w struct {
		JobID      string              `json:"jobId"`
		Deliveries []rendobar.Delivery `json:"deliveries"`
	}
	if err := e.decode(&w, JobDeliverySucceeded, JobDeliveryFailed, JobDeliveriesSettled); err != nil {
		return nil, err
	}
	out := &DeliveryEvent{JobID: w.JobID, Deliveries: w.Deliveries}
	if e.Type != JobDeliveriesSettled {
		d := new(rendobar.Delivery)
		if err := json.Unmarshal(e.Data, d); err != nil {
			return nil, err
		}
		out.Delivery = d
	}
	return out, nil
}

// BalanceEvent is the data of balance.low and balance.depleted.
type BalanceEvent struct {
	Nanodollars int64  // the balance left, where 1 USD is 1,000,000,000
	Formatted   string // the balance for display
	// ThresholdNanodollars is the alert level that balance.low crossed, or 0
	// on balance.depleted.
	ThresholdNanodollars int64
	ThresholdFormatted   string
	// The job whose charge set off the event.
	TriggeredByJobID   string
	TriggeredByJobType string
}

// Balance reads the data of a balance event. It fails for any other event.
func (e *Event) Balance() (*BalanceEvent, error) {
	var w struct {
		Balance            float64  `json:"balance"`
		BalanceFormatted   string   `json:"balanceFormatted"`
		Threshold          *float64 `json:"threshold"`
		ThresholdFormatted string   `json:"thresholdFormatted"`
		TriggeredByJobID   string   `json:"triggeredByJobId"`
		TriggeredByJobType string   `json:"triggeredByJobType"`
	}
	if err := e.decode(&w, BalanceLow, BalanceDepleted); err != nil {
		return nil, err
	}
	out := &BalanceEvent{
		Nanodollars: int64(w.Balance), Formatted: w.BalanceFormatted, ThresholdFormatted: w.ThresholdFormatted,
		TriggeredByJobID: w.TriggeredByJobID, TriggeredByJobType: w.TriggeredByJobType,
	}
	if w.Threshold != nil {
		out.ThresholdNanodollars = int64(*w.Threshold)
	}
	return out, nil
}

func unixMs(p *float64) time.Time {
	if p == nil {
		return time.Time{}
	}
	return time.UnixMilli(int64(*p))
}
