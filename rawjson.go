package rendobar

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
)

// getJSON sends a GET and decodes the JSON body into out. Job and account reads
// go this way rather than through the generated core, which decodes integers
// into Go's int: on a 32-bit platform a Unix-ms timestamp or a byte quota
// overflows it. The public types decode into int64.
func (c *Client) getJSON(ctx context.Context, path string, query url.Values, out any) error {
	return c.doJSON(ctx, http.MethodGet, path, query, nil, out)
}

// doJSON sends one request with an optional JSON body and decodes a JSON
// response into out, unless out is nil. Every resource method but the job
// submit, cancel and type reads goes this way, for the reason getJSON gives.
func (c *Client) doJSON(ctx context.Context, method, path string, query url.Values, body, out any) error {
	u := c.baseURL + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return local(CodeInvalidArgument, "the request body does not encode to JSON: %v", err)
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, reader)
	if err != nil {
		return err
	}
	req.Header = c.header.Clone()
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := c.api.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(res.Body)
	if err != nil {
		return err
	}
	if res.StatusCode < 200 || res.StatusCode > 299 {
		return apiError(res.StatusCode, res.Header, raw)
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("rendobar: unreadable response from %s: %w", path, err)
	}
	return nil
}

// call sends a request that needs an API key and returns the data of the
// response envelope, {"data": ...}, as T.
func call[T any](ctx context.Context, c *Client, method, path string, query url.Values, body any) (*T, error) {
	if err := c.needKey(); err != nil {
		return nil, err
	}
	var res struct {
		Data *T `json:"data"`
	}
	if err := c.doJSON(ctx, method, path, query, body, &res); err != nil {
		return nil, err
	}
	if res.Data == nil {
		return nil, fmt.Errorf("rendobar: the response from %s %s has no data", method, path)
	}
	return res.Data, nil
}

// callList is call for a response whose data is a list. A null list reads as empty.
func callList[T any](ctx context.Context, c *Client, method, path string, query url.Values, body any) ([]*T, error) {
	if err := c.needKey(); err != nil {
		return nil, err
	}
	var res struct {
		Data []*T `json:"data"`
	}
	if err := c.doJSON(ctx, method, path, query, body, &res); err != nil {
		return nil, err
	}
	if res.Data == nil {
		res.Data = []*T{}
	}
	return res.Data, nil
}

// callPage is callList for a list that comes with meta, such as a page's total.
func callPage[T, M any](ctx context.Context, c *Client, path string, query url.Values) ([]*T, M, error) {
	var res struct {
		Data []*T `json:"data"`
		Meta M    `json:"meta"`
	}
	if err := c.needKey(); err != nil {
		return nil, res.Meta, err
	}
	if err := c.doJSON(ctx, http.MethodGet, path, query, nil, &res); err != nil {
		return nil, res.Meta, err
	}
	if res.Data == nil {
		res.Data = []*T{}
	}
	return res.Data, res.Meta, nil
}

// send makes a call whose response body says nothing the caller needs, such as a delete.
func send(ctx context.Context, c *Client, method, path string, body any) error {
	if err := c.needKey(); err != nil {
		return err
	}
	return c.doJSON(ctx, method, path, nil, body, nil)
}

// validator is a generated request body: its validate collects what is wrong with it.
type validator interface{ validate(c *check, path string) }

// valid checks a request body before anything is sent. A body that fails
// returns an INVALID_ARGUMENT *Error with one Problem each.
func valid(v validator) error {
	var c check
	v.validate(&c, "")
	return c.err()
}

// segment checks an id before it goes into a URL path and escapes it. what
// names it in the error, such as "webhook endpoint id". An id with a slash, an
// empty one, "." or ".." could reach another route, so each is refused with
// an INVALID_ARGUMENT *Error before any request.
func segment(what, id string) (string, error) {
	if !jobID.MatchString(id) || id == "." || id == ".." {
		return "", local(CodeInvalidArgument, "%s %q is not valid: use letters, digits, '.', '_' and '-'", what, id)
	}
	return url.PathEscape(id), nil
}

// paging refuses a negative limit or offset, or page, before any request.
func paging(what string, values ...int) error {
	for _, v := range values {
		if v < 0 {
			return local(CodeInvalidArgument, "%s must not be negative, got %v", what, values)
		}
	}
	return nil
}

// setInt adds a positive integer to a query. 0 means the API's default.
func setInt(q url.Values, key string, v int) {
	if v > 0 {
		q.Set(key, strconv.Itoa(v))
	}
}

// setString adds a non-empty string to a query.
func setString[S ~string](q url.Values, key string, v S) {
	if v != "" {
		q.Set(key, string(v))
	}
}
