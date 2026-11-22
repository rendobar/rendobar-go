package rendobar

import (
	"context"
	"net/http"
	"net/url"
)

// Webhooks manages webhook endpoints and their deliveries. To verify and read
// a delivery your server receives, use package webhooks.
type Webhooks struct{ client *Client }

func endpointPath(id string) (string, error) {
	seg, err := segment("webhook endpoint id", id)
	if err != nil {
		return "", err
	}
	return "/webhooks/endpoints/" + seg, nil
}

// Create adds an endpoint. Its SigningSecret is the secret webhooks.Parse
// verifies deliveries with, shown in full only here and by RotateSecret.
func (w *Webhooks) Create(ctx context.Context, p CreateWebhookParams) (*WebhookEndpoint, error) {
	if err := valid(p); err != nil {
		return nil, err
	}
	return call[WebhookEndpoint](ctx, w.client, http.MethodPost, "/webhooks/endpoints", nil, p)
}

// WebhookEndpointList is every webhook endpoint of the workspace.
type WebhookEndpointList struct {
	Endpoints []*WebhookEndpoint
	Total     int64
}

// List returns every endpoint. SigningSecret is masked.
func (w *Webhooks) List(ctx context.Context) (*WebhookEndpointList, error) {
	items, meta, err := callPage[WebhookEndpoint, struct {
		Total int64 `json:"total"`
	}](ctx, w.client, "/webhooks/endpoints", nil)
	if err != nil {
		return nil, err
	}
	return &WebhookEndpointList{Endpoints: items, Total: meta.Total}, nil
}

// Get returns one endpoint. SigningSecret is masked.
func (w *Webhooks) Get(ctx context.Context, id string) (*WebhookEndpoint, error) {
	path, err := endpointPath(id)
	if err != nil {
		return nil, err
	}
	return call[WebhookEndpoint](ctx, w.client, http.MethodGet, path, nil, nil)
}

// Update changes an endpoint's name, URL or events, or turns it off or on.
// Fields left zero stay as they are.
func (w *Webhooks) Update(ctx context.Context, id string, p UpdateWebhookParams) (*WebhookEndpoint, error) {
	path, err := endpointPath(id)
	if err != nil {
		return nil, err
	}
	if err := valid(p); err != nil {
		return nil, err
	}
	return call[WebhookEndpoint](ctx, w.client, http.MethodPatch, path, nil, p)
}

// Delete removes an endpoint. Deliveries to it stop.
func (w *Webhooks) Delete(ctx context.Context, id string) error {
	path, err := endpointPath(id)
	if err != nil {
		return err
	}
	return send(ctx, w.client, http.MethodDelete, path, nil)
}

// RotateSecret gives an endpoint a new signing secret, returned in full in
// SigningSecret. For a while deliveries are signed with both, the old one in
// X-Rendobar-Signature-Previous, so a receiver keeps working while you switch.
func (w *Webhooks) RotateSecret(ctx context.Context, id string) (*WebhookEndpoint, error) {
	path, err := endpointPath(id)
	if err != nil {
		return nil, err
	}
	return call[WebhookEndpoint](ctx, w.client, http.MethodPost, path+"/secret", nil, nil)
}

// Test sends the endpoint a test event and reports how it answered.
func (w *Webhooks) Test(ctx context.Context, id string) (*WebhookTestResult, error) {
	path, err := endpointPath(id)
	if err != nil {
		return nil, err
	}
	return call[WebhookTestResult](ctx, w.client, http.MethodPost, path+"/test", nil, nil)
}

// ListDeliveriesParams filters Webhooks.ListDeliveries. The zero value lists every delivery.
type ListDeliveriesParams struct {
	EndpointID string                // one endpoint's deliveries, or "" for all
	Event      WebhookEvent          // one event's, such as webhooks.JobCompleted, or "" for all
	Status     WebhookDeliveryStatus // such as WebhookDeliveryStatusFailed, or "" for all
	JobID      string                // one job's, or "" for all
	// Limit is the page size, up to 100. 0 is the API's default of 50.
	Limit int
	// Offset skips that many matching deliveries.
	Offset int
}

// DeliveryPage is one page of webhook deliveries, newest first.
type DeliveryPage struct {
	Deliveries []*WebhookDelivery
	Meta       DeliveryPageMeta
}

// ListDeliveries returns one page of deliveries. A negative Limit or Offset
// is an INVALID_ARGUMENT *Error.
func (w *Webhooks) ListDeliveries(ctx context.Context, p ListDeliveriesParams) (*DeliveryPage, error) {
	if err := paging("ListDeliveriesParams Limit and Offset", p.Limit, p.Offset); err != nil {
		return nil, err
	}
	q := url.Values{}
	setString(q, "endpointId", p.EndpointID)
	setString(q, "event", p.Event)
	setString(q, "status", p.Status)
	setString(q, "jobId", p.JobID)
	setInt(q, "limit", p.Limit)
	setInt(q, "offset", p.Offset)
	items, meta, err := callPage[WebhookDelivery, DeliveryPageMeta](ctx, w.client, "/webhooks/deliveries", q)
	if err != nil {
		return nil, err
	}
	return &DeliveryPage{Deliveries: items, Meta: meta}, nil
}

// RetryDelivery sends a delivery again, as a new attempt of the same delivery.
func (w *Webhooks) RetryDelivery(ctx context.Context, id string) (*RetriedDelivery, error) {
	seg, err := segment("webhook delivery id", id)
	if err != nil {
		return nil, err
	}
	return call[RetriedDelivery](ctx, w.client, http.MethodPost, "/webhooks/deliveries/"+seg+"/retry", nil, nil)
}
