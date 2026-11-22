// Account-admin reads what the workspace may do and which job types it can
// run, then reads one type's params.
//
//	RENDOBAR_API_KEY=rb_... go run ./account-admin -mode live
//
// No jobs. The Go SDK reads the account's plan, limits and features; it has
// no API keys, billing, usage or org settings API (see ../README.md).
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

func run(mode string) error {
	if mode != "mock" && mode != "live" {
		return fmt.Errorf("-mode must be mock or live, got %q", mode)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	var opts []rendobar.Option
	if u := os.Getenv("RENDOBAR_API_URL"); u != "" {
		opts = append(opts, rendobar.WithBaseURL(u))
	}
	rb := rendobar.New(opts...)

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
	fmt.Println("SKIP (sdk: the Go SDK has no billing, usage, API keys or org settings API) billing, usage, create and revoke a key, org settings")

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
	if mode == "mock" {
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
	if mode == "live" && !slices.ContainsFunc(schema.Fields, func(f rendobar.SchemaField) bool { return f.Name == "command" && f.Required }) {
		return errors.New("the ffmpeg schema has no required command field")
	}
	fmt.Printf("%s takes %d params, first %q\n", want, len(schema.Fields), schema.Fields[0].Name)
	return nil
}
