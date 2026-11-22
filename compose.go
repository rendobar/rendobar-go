package rendobar

import (
	"context"
	"encoding/json"
	"net/http"
)

// Compose answers questions about compose jobs without rendering anything.
type Compose struct{ client *Client }

// Effects lists every built-in effect, with its params, ranges, units,
// defaults and which ones take keyframes.
func (c *Compose) Effects(ctx context.Context) (*ComposeEffects, error) {
	return call[ComposeEffects](ctx, c.client, http.MethodGet, "/compose/effects", nil, nil)
}

// EngineEffects lists every filter and service an engine effect may name,
// with its params. A filter a built-in effect covers is not listed.
func (c *Compose) EngineEffects(ctx context.Context) (*ComposeEngineEffects, error) {
	return call[ComposeEngineEffects](ctx, c.client, http.MethodGet, "/compose/effects/engine", nil, nil)
}

// ComposeValidateParams is what Compose.Validate checks.
type ComposeValidateParams struct {
	// Params are exactly the compose params you would submit, the Fields of
	// CustomParams{Type: JobTypeCompose}.
	Params map[string]any
}

// ComposeValidation is what Compose.Validate found. Branch on Valid.
type ComposeValidation struct {
	Valid bool
	// Params are the params as a render reads them: defaults filled in, and a
	// timeline in an older shape rewritten into the current one. Submit these.
	// nil when the API could not read the params at all.
	Params map[string]any
	// Upgraded names what was rewritten from an older shape.
	Upgraded []string
	// Warnings are things a render accepts but you may not mean.
	Warnings []Problem
	// Problems are why the params are not valid, at least one when Valid is
	// false. A video above the plan has a problem at "output" whose UpgradeTo
	// names the plan that renders it.
	Problems []Problem
	// Price is what a render costs, when Valid is true.
	Price *ComposePrice
}

// ComposePrice is the price of a compose render.
type ComposePrice struct {
	PerMinuteUSD float64 // one minute of output, billed per second
	MinSeconds   int64   // the shortest length billed
}

// UnmarshalJSON reads either outcome, valid or not.
func (v *ComposeValidation) UnmarshalJSON(b []byte) error {
	var w struct {
		Valid    bool            `json:"valid"`
		Params   map[string]any  `json:"params"`
		Upgraded []string        `json:"upgraded"`
		Warnings json.RawMessage `json:"warnings"`
		Problems json.RawMessage `json:"problems"`
		Price    *struct {
			PerMinuteUSD float64 `json:"perMinuteUsd"`
			MinSeconds   int64   `json:"minSeconds"`
		} `json:"price"`
	}
	if err := json.Unmarshal(b, &w); err != nil {
		return err
	}
	*v = ComposeValidation{Valid: w.Valid, Params: w.Params, Upgraded: w.Upgraded,
		Warnings: problems(w.Warnings), Problems: problems(w.Problems)}
	if w.Price != nil {
		v.Price = &ComposePrice{PerMinuteUSD: w.Price.PerMinuteUSD, MinSeconds: w.Price.MinSeconds}
	}
	return nil
}

// Validate checks compose params without rendering them. It is free, and
// nothing is stored. Params that are not valid are not an error: Validate
// returns them with Valid false and the Problems.
func (c *Compose) Validate(ctx context.Context, p ComposeValidateParams) (*ComposeValidation, error) {
	if p.Params == nil {
		return nil, local(CodeInvalidArgument, "ComposeValidateParams.Params is required")
	}
	body := struct {
		Params map[string]any `json:"params"`
	}{p.Params}
	return call[ComposeValidation](ctx, c.client, http.MethodPost, "/compose/validate", nil, body)
}
