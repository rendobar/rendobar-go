package rendobar

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
)

// getJSON sends a GET and decodes the JSON body into out. Job and account reads
// go this way rather than through the generated core, which decodes integers
// into Go's int: on a 32-bit platform a Unix-ms timestamp or a byte quota
// overflows it. The public types decode into int64.
func (c *Client) getJSON(ctx context.Context, path string, query url.Values, out any) error {
	u := c.baseURL + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	req.Header = c.header.Clone()
	res, err := c.api.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	body, err := io.ReadAll(res.Body)
	if err != nil {
		return err
	}
	if res.StatusCode < 200 || res.StatusCode > 299 {
		return apiError(res.StatusCode, res.Header, body)
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("rendobar: unreadable response from %s: %w", path, err)
	}
	return nil
}
