// Account-admin reads what the workspace may do and what it has spent, its
// API keys, notifications, storage and settings, and what jobs it can run.
// It changes one org setting and puts it back.
//
//	RENDOBAR_API_KEY=rb_... go run ./account-admin -mode live
//
// No jobs. Checkout, subscription and payment-method changes, org create and
// delete, and member invitations run against the mock only, never live.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"slices"
	"time"

	"github.com/rendobar/rendobar-go"
)

func main() {
	mode := flag.String("mode", "mock", "mock (the conformance mock) or live (a real API)")
	flag.Parse()
	if err := run(*mode); err != nil {
		fmt.Fprintln(os.Stderr, "FAIL:", err)
		os.Exit(1)
	}
	fmt.Println("PASS account-admin")
}

func run(mode string) (err error) {
	if mode != "mock" && mode != "live" {
		return fmt.Errorf("-mode must be mock or live, got %q", mode)
	}
	mock := mode == "mock"
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	var opts []rendobar.Option
	if u := os.Getenv("RENDOBAR_API_URL"); u != "" {
		opts = append(opts, rendobar.WithBaseURL(u))
	}
	rb := rendobar.New(opts...)

	for _, step := range []func(context.Context, *rendobar.Client, bool) error{
		capabilities, billing, keys, orgs, notifications, storage, jobTypes, compose,
	} {
		if err := step(ctx, rb, mock); err != nil {
			return err
		}
	}
	return nil
}

func capabilities(ctx context.Context, rb *rendobar.Client, _ bool) error {
	caps, err := rb.Account.Capabilities(ctx)
	if err != nil {
		return fmt.Errorf("capabilities: %w", err)
	}
	if caps.Plan.Slug == "" || caps.Plan.Name == "" {
		return errors.New("the account has no plan")
	}
	if caps.Limits.ConcurrentJobs <= 0 || caps.Limits.MaxJobTimeout <= 0 {
		return fmt.Errorf("limits %+v, want concurrent jobs and a job timeout above 0", caps.Limits)
	}
	fmt.Printf("plan %s (%s): %d concurrent jobs, %s per job, webhooks %v, GPU %v\n",
		caps.Plan.Name, caps.Plan.Slug, caps.Limits.ConcurrentJobs, caps.Limits.MaxJobTimeout, caps.Features.Webhooks, caps.Features.GPU)
	return nil
}

// billing reads the balance, usage and history. The calls that charge or
// change a subscription run against the mock only.
func billing(ctx context.Context, rb *rendobar.Client, mock bool) error {
	state, err := rb.Billing.State(ctx)
	if err != nil {
		return fmt.Errorf("billing state: %w", err)
	}
	if state.Plan == nil || state.Plan.Slug == "" || state.Balance == nil || state.Usage == nil {
		return fmt.Errorf("billing state %+v has no plan, balance or usage", state)
	}
	fmt.Printf("balance $%.2f on %s, %d jobs this period\n", state.Balance.Amount, state.Plan.Slug, state.Usage.JobCount)

	end := time.Now().UTC()
	usage, err := rb.Billing.Usage(ctx, rendobar.UsageParams{Start: end.AddDate(0, 0, -30), End: end})
	if err != nil {
		return fmt.Errorf("usage: %w", err)
	}
	if usage.TotalJobs < 0 || usage.TotalAmountCharged < 0 || usage.TotalAmountFormatted == "" {
		return fmt.Errorf("usage %+v", usage)
	}
	fmt.Println("last 30 days:", usage.TotalJobs, "jobs,", usage.TotalAmountFormatted, "over", len(usage.ByJobType), "job types")

	clients, err := rb.Billing.UsageByClient(ctx, rendobar.UsageByClientParams{Days: 30})
	if err != nil {
		return fmt.Errorf("usage by client: %w", err)
	}
	for _, c := range clients {
		if c.Client == "" {
			return fmt.Errorf("usage row %+v names no client", c)
		}
	}
	fmt.Println("usage from", len(clients), "clients")

	txs, err := rb.Billing.Transactions(ctx, rendobar.PageParams{Limit: 5})
	if err != nil {
		return fmt.Errorf("transactions: %w", err)
	}
	if int64(len(txs.Transactions)) > txs.Meta.Total {
		return fmt.Errorf("%d transactions on a page of %d in all", len(txs.Transactions), txs.Meta.Total)
	}
	invoices, err := rb.Billing.History(ctx, rendobar.PageParams{Limit: 5})
	if err != nil {
		return fmt.Errorf("invoices: %w", err)
	}
	fmt.Println(txs.Meta.Total, "transactions and", invoices.Meta.Total, "invoices")

	if !mock {
		fmt.Println("SKIP (live: never charges or changes billing) checkout, plan change, subscription, credit schedule, payment methods, portal")
		return nil
	}
	if _, err := rb.Billing.CheckoutCredits(ctx, rendobar.CheckoutCreditsParams{Amount: rendobar.Ptr(25.0)}); err != nil {
		return fmt.Errorf("checkout credits: %w", err)
	}
	plan, err := rb.Billing.CheckoutPlan(ctx, rendobar.CheckoutPlanParams{Plan: rendobar.PlanPro})
	if err != nil {
		return fmt.Errorf("checkout plan: %w", err)
	}
	switch plan.Kind {
	case rendobar.PlanCheckoutKindCheckout:
		fmt.Println("plan checkout to open:", plan.Checkout.CheckoutURL)
	case rendobar.PlanCheckoutKindChanged:
		fmt.Println("plan changed to", plan.Changed.Plan)
	default:
		fmt.Println("plan checkout of a kind newer than this SDK:", plan.Kind)
	}
	methods, err := rb.Billing.PaymentMethods(ctx)
	if err != nil {
		return fmt.Errorf("payment methods: %w", err)
	}
	if len(methods) == 0 {
		return errors.New("no payment methods")
	}
	portal, err := rb.Billing.PortalURL(ctx)
	if err != nil || portal == "" {
		return fmt.Errorf("portal: %q %v", portal, err)
	}
	for name, call := range map[string]func() error{
		"cancel subscription":     func() error { return rb.Billing.CancelSubscription(ctx) },
		"reactivate subscription": func() error { return rb.Billing.ReactivateSubscription(ctx) },
		"cancel credit schedule":  func() error { return rb.Billing.CancelCreditSchedule(ctx) },
		"set default method":      func() error { return rb.Billing.SetDefaultPaymentMethod(ctx, methods[0].ID) },
		"delete payment method":   func() error { return rb.Billing.DeletePaymentMethod(ctx, methods[0].ID) },
	} {
		if err := call(); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
	}
	fmt.Println("checkouts, subscription, credit schedule and payment methods answered (mock)")
	return nil
}

// keys lists the workspace's keys. An API key cannot create one, so creating,
// updating and revoking a key run against the mock only.
func keys(ctx context.Context, rb *rendobar.Client, mock bool) error {
	list, err := rb.APIKeys.List(ctx)
	if err != nil {
		return fmt.Errorf("list keys: %w", err)
	}
	if len(list) == 0 {
		return errors.New("no API keys, not even the one this runs with")
	}
	for _, k := range list {
		if k.ID == "" || k.Prefix == "" || k.Key != "" {
			return fmt.Errorf("key %s (prefix %q) is listed without an id or prefix, or with its value", k.ID, k.Prefix)
		}
	}
	fmt.Println(len(list), "API keys")
	if !mock {
		fmt.Println("SKIP (live: an API key cannot create keys) create, update and revoke a key")
		return nil
	}
	created, err := rb.APIKeys.Create(ctx, rendobar.CreateAPIKeyParams{Name: "examples-go", Scopes: []string{"jobs:read"}})
	if err != nil {
		return fmt.Errorf("create key: %w", err)
	}
	if created.Key == "" {
		return errors.New("a created key has no value")
	}
	if _, err := rb.APIKeys.Update(ctx, created.ID, rendobar.UpdateAPIKeyParams{Enabled: rendobar.Ptr(false)}); err != nil {
		return fmt.Errorf("disable key: %w", err)
	}
	if err := rb.APIKeys.Revoke(ctx, created.ID); err != nil {
		return fmt.Errorf("revoke key: %w", err)
	}
	fmt.Println("created, disabled and revoked a key (mock)")
	return nil
}

// orgs reads the workspace and its settings, flips one setting and puts it
// back. Creating and deleting workspaces and inviting people run against the mock only.
func orgs(ctx context.Context, rb *rendobar.Client, mock bool) (err error) {
	list, err := rb.Orgs.List(ctx)
	if err != nil {
		return fmt.Errorf("list orgs: %w", err)
	}
	current, err := rb.Orgs.Current(ctx)
	if err != nil {
		return fmt.Errorf("current org: %w", err)
	}
	if current.Org == nil || current.Org.ID == "" || current.Org.MemberRole == "" {
		return fmt.Errorf("current org %+v has no id or role", current.Org)
	}
	if !mock && !slices.ContainsFunc(list, func(o *rendobar.Org) bool { return o.ID == current.Org.ID }) {
		return fmt.Errorf("org %s is not among the %d listed", current.Org.ID, len(list))
	}
	fmt.Printf("org %s (%s), role %s, in %d orgs\n", current.Org.Name, current.Org.ID, current.Org.MemberRole, len(list))

	before, err := rb.Orgs.GetSettings(ctx)
	if err != nil {
		return fmt.Errorf("settings: %w", err)
	}
	if before.DefaultRegion == "" {
		return fmt.Errorf("settings %+v have no default region", before)
	}
	defer func() {
		// Put the setting back even when a step failed.
		restored, rerr := rb.Orgs.UpdateSettings(context.Background(), rendobar.UpdateOrgSettingsParams{BillingEmailsEnabled: rendobar.Ptr(before.BillingEmailsEnabled)})
		switch {
		case rerr != nil && err == nil:
			err = fmt.Errorf("restore settings: %w", rerr)
		case rerr == nil && !mock && restored.BillingEmailsEnabled != before.BillingEmailsEnabled:
			err = errors.New("the billing emails setting was not restored")
		case rerr == nil:
			fmt.Println("restored billing emails to", before.BillingEmailsEnabled)
		}
	}()
	flipped, err := rb.Orgs.UpdateSettings(ctx, rendobar.UpdateOrgSettingsParams{BillingEmailsEnabled: rendobar.Ptr(!before.BillingEmailsEnabled)})
	if err != nil {
		return fmt.Errorf("update settings: %w", err)
	}
	if !mock && flipped.BillingEmailsEnabled == before.BillingEmailsEnabled {
		return errors.New("the billing emails setting did not change")
	}
	fmt.Println("region", before.DefaultRegion, "; billing emails turned", flipped.BillingEmailsEnabled)

	if !mock {
		fmt.Println("SKIP (live: never creates or deletes workspaces or invites people) org create, rename, delete, invite, role change, removal")
		return nil
	}
	org, err := rb.Orgs.Create(ctx, rendobar.CreateOrgParams{Name: "Examples Go", Slug: "examples-go"})
	if err != nil {
		return fmt.Errorf("create org: %w", err)
	}
	if _, err := rb.Orgs.Update(ctx, rendobar.UpdateOrgParams{Name: "Examples Go 2"}); err != nil {
		return fmt.Errorf("rename org: %w", err)
	}
	invite, err := rb.Orgs.Members.Invite(ctx, rendobar.InviteMemberParams{Email: "someone@example.com", Role: rendobar.MemberRoleMember})
	if err != nil {
		return fmt.Errorf("invite: %w", err)
	}
	for name, call := range map[string]func() error{
		"change role": func() error {
			return rb.Orgs.Members.ChangeRole(ctx, "mem_examples", rendobar.ChangeRoleParams{Role: rendobar.MemberRoleAdmin})
		},
		"remove member":     func() error { return rb.Orgs.Members.Remove(ctx, "mem_examples") },
		"revoke invitation": func() error { return rb.Orgs.Invitations.Revoke(ctx, invite.ID) },
		"delete org":        func() error { return rb.Orgs.Delete(ctx) },
	} {
		if err := call(); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
	}
	fmt.Println("created", org.ID, "invited, changed a role, removed a member, revoked the invite, deleted it (mock)")
	return nil
}

// notifications lists the newest notifications and marks an already read one
// read again, which changes nothing.
func notifications(ctx context.Context, rb *rendobar.Client, mock bool) error {
	list, err := rb.Notifications.List(ctx)
	if err != nil {
		return fmt.Errorf("notifications: %w", err)
	}
	if list.Unread < 0 {
		return fmt.Errorf("unread count %d", list.Unread)
	}
	fmt.Println(len(list.Notifications), "notifications,", list.Unread, "unread")
	i := slices.IndexFunc(list.Notifications, func(n *rendobar.Notification) bool { return n.Read || mock })
	if i < 0 {
		fmt.Println("SKIP (live: no read notification to mark again) mark read")
		return nil
	}
	read, err := rb.Notifications.MarkRead(ctx, rendobar.MarkReadParams{IDs: []string{list.Notifications[i].ID}})
	if err != nil {
		return fmt.Errorf("mark read: %w", err)
	}
	fmt.Println("marked", read.Read, "read")
	return nil
}

// storage lists the storage connections and reads the first. Connecting a
// bucket needs real credentials, so the writes run against the mock only.
func storage(ctx context.Context, rb *rendobar.Client, mock bool) error {
	list, err := rb.Storage.List(ctx)
	var apiErr *rendobar.Error
	if errors.As(err, &apiErr) && apiErr.Code == rendobar.CodeInsufficientScope {
		// A key holds only the scopes it was created with.
		fmt.Println("SKIP (key: no storage:read scope) storage connections:", apiErr.Message)
		return nil
	}
	if err != nil {
		return fmt.Errorf("storage: %w", err)
	}
	fmt.Println(list.Total, "storage connections")
	if len(list.Connections) > 0 {
		first, err := rb.Storage.Get(ctx, list.Connections[0].ID)
		if err != nil {
			return fmt.Errorf("storage get: %w", err)
		}
		stats, err := rb.Storage.Stats(ctx, first.ID)
		if err != nil {
			return fmt.Errorf("storage stats: %w", err)
		}
		fmt.Println("connection", first.ID, "on", first.Provider, "delivered", stats.Deliveries.Delivered, "in", stats.WindowDays, "days")
	}
	if !mock {
		fmt.Println("SKIP (live: needs a bucket's credentials) connect, test, list objects, update and remove storage")
		return nil
	}
	conn, err := rb.Storage.Create(ctx, rendobar.CreateStorageS3Params{
		ID: "examples-go", Bucket: "examples-go", Region: "us-east-1",
		Credentials: rendobar.StorageKeys{AccessKeyID: "AKIAEXAMPLE", SecretAccessKey: "example"},
	})
	if err != nil {
		return fmt.Errorf("connect storage: %w", err)
	}
	if _, err := rb.Storage.Test(ctx, conn.ID); err != nil {
		return fmt.Errorf("test storage: %w", err)
	}
	if _, err := rb.Storage.ListObjects(ctx, conn.ID, rendobar.ListObjectsParams{Prefix: "out/"}); err != nil {
		return fmt.Errorf("list objects: %w", err)
	}
	if _, err := rb.Storage.Update(ctx, conn.ID, rendobar.UpdateStorageParams{OnConflict: rendobar.StorageOnConflictSuffix}); err != nil {
		return fmt.Errorf("update storage: %w", err)
	}
	if err := rb.Storage.Delete(ctx, conn.ID); err != nil {
		return fmt.Errorf("remove storage: %w", err)
	}
	fmt.Println("connected, tested, listed, updated and removed storage (mock)")
	return nil
}

func jobTypes(ctx context.Context, rb *rendobar.Client, mock bool) error {
	// Types and Schema are public: a client without a key can call them too.
	types, err := rb.Jobs.Types(ctx)
	if err != nil {
		return fmt.Errorf("types: %w", err)
	}
	if len(types) == 0 {
		return errors.New("no job types")
	}
	for _, t := range types {
		if t.Type == "" || t.Summary == "" {
			return fmt.Errorf("job type %+v has no name or summary", t)
		}
	}
	fmt.Println(len(types), "job types, first", types[0].Type)

	want := rendobar.JobTypeFfmpeg
	if mock {
		// The mock lists one synthesized type, so read the schema of that one.
		want = types[0].Type
	} else if !slices.ContainsFunc(types, func(t rendobar.JobTypeInfo) bool { return t.Type == want }) {
		return fmt.Errorf("the job types do not include %s", want)
	}
	schema, err := rb.Jobs.Schema(ctx, want)
	if err != nil {
		return fmt.Errorf("schema %s: %w", want, err)
	}
	if len(schema.Fields) == 0 || len(schema.JSONSchema) == 0 {
		return fmt.Errorf("schema %s has no fields", want)
	}
	if !mock && !slices.ContainsFunc(schema.Fields, func(f rendobar.SchemaField) bool { return f.Name == "command" && f.Required }) {
		return errors.New("the ffmpeg schema has no required command field")
	}
	fmt.Printf("%s takes %d params, first %q\n", want, len(schema.Fields), schema.Fields[0].Name)
	return nil
}

// compose reads the effect catalogue and checks a timeline without rendering it, for free.
func compose(ctx context.Context, rb *rendobar.Client, mock bool) error {
	effects, err := rb.Compose.Effects(ctx)
	if err != nil {
		return fmt.Errorf("compose effects: %w", err)
	}
	if len(effects.Effects) == 0 || effects.Effects[0].Type == "" {
		return errors.New("no compose effects")
	}
	engine, err := rb.Compose.EngineEffects(ctx)
	if err != nil {
		return fmt.Errorf("engine effects: %w", err)
	}
	fmt.Println(len(effects.Effects), "compose effects and", len(engine.Filters), "engine filters")

	check, err := rb.Compose.Validate(ctx, rendobar.ComposeValidateParams{Params: map[string]any{"timeline": "not a timeline"}})
	if err != nil {
		return fmt.Errorf("compose validate: %w", err)
	}
	switch {
	case mock:
		fmt.Println("SKIP (mock: answers every check as valid) an invalid timeline names its problem")
	case check.Valid || len(check.Problems) == 0:
		return fmt.Errorf("an invalid timeline came back valid %v with %d problems", check.Valid, len(check.Problems))
	default:
		fmt.Println("invalid timeline:", check.Problems[0].Path, check.Problems[0].Message)
	}
	return nil
}
