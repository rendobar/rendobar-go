package rendobar

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// Billing reads the workspace's balance, plan, usage and invoices, and starts
// checkouts and subscription changes.
type Billing struct{ client *Client }

// State returns the balance, the plan and its limits, the subscription and this period's usage.
func (b *Billing) State(ctx context.Context) (*BillingState, error) {
	return call[BillingState](ctx, b.client, http.MethodGet, "/billing/state", nil, nil)
}

// UsageParams bounds Billing.Usage. The zero value is the API's default range.
type UsageParams struct {
	// Start is the first day counted, in UTC. The zero time leaves it to the API.
	Start time.Time
	// End is the last day counted, in UTC. The zero time leaves it to the API.
	End time.Time
}

// Usage returns jobs, spend and compute time over a range of days, in total,
// by job type and day by day.
func (b *Billing) Usage(ctx context.Context, p UsageParams) (*Usage, error) {
	q := url.Values{}
	if !p.Start.IsZero() {
		q.Set("start", p.Start.UTC().Format(time.DateOnly))
	}
	if !p.End.IsZero() {
		q.Set("end", p.End.UTC().Format(time.DateOnly))
	}
	return call[Usage](ctx, b.client, http.MethodGet, "/billing/usage", q, nil)
}

// UsageByClientParams bounds Billing.UsageByClient.
type UsageByClientParams struct {
	// Days counts back from today. 0 is the API's default. A negative value is an INVALID_ARGUMENT *Error.
	Days int
}

// UsageByClient returns jobs and spend grouped by the client that submitted
// them, such as the dashboard, an SDK, the CLI, MCP or n8n.
func (b *Billing) UsageByClient(ctx context.Context, p UsageByClientParams) ([]*ClientUsage, error) {
	if err := paging("UsageByClientParams.Days", p.Days); err != nil {
		return nil, err
	}
	q := url.Values{}
	if p.Days > 0 {
		q.Set("days", strconv.Itoa(p.Days))
	}
	return callList[ClientUsage](ctx, b.client, http.MethodGet, "/billing/usage-by-client", q, nil)
}

// PageParams picks one page of a list paged by number, such as Billing.Transactions.
type PageParams struct {
	// Page is the page number, from 1. 0 is the first page.
	Page int
	// Limit is the page size. 0 is the API's default.
	Limit int
}

func (p PageParams) query() (url.Values, error) {
	if err := paging("PageParams Page and Limit", p.Page, p.Limit); err != nil {
		return nil, err
	}
	q := url.Values{}
	setInt(q, "page", p.Page)
	setInt(q, "limit", p.Limit)
	return q, nil
}

// TransactionPage is one page of balance transactions, newest first.
type TransactionPage struct {
	Transactions []*Transaction
	Meta         PageMeta
}

// Transactions returns one page of credits and debits to the balance.
func (b *Billing) Transactions(ctx context.Context, p PageParams) (*TransactionPage, error) {
	q, err := p.query()
	if err != nil {
		return nil, err
	}
	items, meta, err := callPage[Transaction, PageMeta](ctx, b.client, "/billing/transactions", q)
	if err != nil {
		return nil, err
	}
	return &TransactionPage{Transactions: items, Meta: meta}, nil
}

// InvoicePage is one page of invoices, newest first.
type InvoicePage struct {
	Invoices []*Invoice
	Meta     PageMeta
}

// History returns one page of invoices: plan charges and credit purchases.
func (b *Billing) History(ctx context.Context, p PageParams) (*InvoicePage, error) {
	q, err := p.query()
	if err != nil {
		return nil, err
	}
	items, meta, err := callPage[Invoice, PageMeta](ctx, b.client, "/billing/history", q)
	if err != nil {
		return nil, err
	}
	return &InvoicePage{Invoices: items, Meta: meta}, nil
}

// CheckoutCredits starts a credit purchase and returns the checkout to open.
// Leave Interval empty for a one-time top-up.
func (b *Billing) CheckoutCredits(ctx context.Context, p CheckoutCreditsParams) (*Checkout, error) {
	if err := valid(p); err != nil {
		return nil, err
	}
	return call[Checkout](ctx, b.client, http.MethodPost, "/billing/checkout/credits", nil, p)
}

// PlanCheckoutKind says which way Billing.CheckoutPlan went. The API may add
// kinds, so keep a default branch.
type PlanCheckoutKind string

// The PlanCheckoutKind values this SDK knows.
const (
	// PlanCheckoutKindCheckout: the workspace had no paid plan. Open PlanCheckout.Checkout.
	PlanCheckoutKindCheckout PlanCheckoutKind = "checkout"
	// PlanCheckoutKindChanged: the workspace's paid plan was moved, billed pro rata.
	PlanCheckoutKindChanged PlanCheckoutKind = "changed"
)

// PlanCheckout is what Billing.CheckoutPlan returns: one of Checkout or
// Changed is set, matching Kind. A kind newer than this SDK sets neither.
type PlanCheckout struct {
	Kind PlanCheckoutKind
	// Checkout is the checkout to open, when Kind is PlanCheckoutKindCheckout.
	Checkout *Checkout
	// Changed names the plan the workspace is on now, when Kind is PlanCheckoutKindChanged.
	Changed *PlanChange
}

// PlanChange is a paid plan moved to another, from Billing.CheckoutPlan.
type PlanChange struct {
	Plan PlanSlug `json:"plan"`
}

// UnmarshalJSON reads the variant Kind names.
func (p *PlanCheckout) UnmarshalJSON(b []byte) error {
	var k struct {
		Kind PlanCheckoutKind `json:"kind"`
	}
	if err := json.Unmarshal(b, &k); err != nil {
		return err
	}
	*p = PlanCheckout{Kind: k.Kind}
	switch k.Kind {
	case PlanCheckoutKindCheckout:
		p.Checkout = new(Checkout)
		return json.Unmarshal(b, p.Checkout)
	case PlanCheckoutKindChanged:
		p.Changed = new(PlanChange)
		return json.Unmarshal(b, p.Changed)
	}
	return nil
}

// CheckoutPlan starts a paid plan, such as PlanPro or PlanStudio. A workspace
// with no paid plan gets a checkout to open. One on another paid plan has its
// subscription moved, billed pro rata.
func (b *Billing) CheckoutPlan(ctx context.Context, p CheckoutPlanParams) (*PlanCheckout, error) {
	if err := valid(p); err != nil {
		return nil, err
	}
	return call[PlanCheckout](ctx, b.client, http.MethodPost, "/billing/checkout/plan", nil, p)
}

// CancelSubscription cancels the plan at the end of the current period. Until
// then the plan stays, and ReactivateSubscription undoes the cancel.
func (b *Billing) CancelSubscription(ctx context.Context) error {
	return send(ctx, b.client, http.MethodPatch, "/billing/subscription", map[string]bool{"cancelAtPeriodEnd": true})
}

// ReactivateSubscription keeps a plan that CancelSubscription set to end.
func (b *Billing) ReactivateSubscription(ctx context.Context) error {
	return send(ctx, b.client, http.MethodPatch, "/billing/subscription", map[string]bool{"cancelAtPeriodEnd": false})
}

// CancelCreditSchedule stops the recurring monthly credit top-up at the end of
// the current period. The plan is separate: CancelSubscription manages it.
func (b *Billing) CancelCreditSchedule(ctx context.Context) error {
	return send(ctx, b.client, http.MethodDelete, "/billing/credit-schedule", nil)
}

// PaymentMethods lists the workspace's saved payment methods.
func (b *Billing) PaymentMethods(ctx context.Context) ([]*PaymentMethod, error) {
	return callList[PaymentMethod](ctx, b.client, http.MethodGet, "/billing/payment-methods", nil, nil)
}

// DeletePaymentMethod removes a saved payment method.
func (b *Billing) DeletePaymentMethod(ctx context.Context, id string) error {
	seg, err := segment("payment method id", id)
	if err != nil {
		return err
	}
	return send(ctx, b.client, http.MethodDelete, "/billing/payment-methods/"+seg, nil)
}

// SetDefaultPaymentMethod makes a saved payment method the one charged.
func (b *Billing) SetDefaultPaymentMethod(ctx context.Context, id string) error {
	seg, err := segment("payment method id", id)
	if err != nil {
		return err
	}
	return send(ctx, b.client, http.MethodPatch, "/billing/payment-methods/"+seg+"/default", nil)
}

// PortalURL returns a link to the payment provider's billing portal, where
// the workspace manages payment methods and invoices.
func (b *Billing) PortalURL(ctx context.Context) (string, error) {
	res, err := call[struct {
		URL string `json:"url"`
	}](ctx, b.client, http.MethodGet, "/billing/payment-methods/portal-url", nil, nil)
	if err != nil {
		return "", err
	}
	return res.URL, nil
}
