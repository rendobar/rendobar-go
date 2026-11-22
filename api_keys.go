package rendobar

import (
	"context"
	"net/http"
)

// APIKeys creates, lists, updates and revokes the workspace's API keys.
type APIKeys struct{ client *Client }

// Create makes an API key and returns it with its value in Key, the only time
// the value is shown. Scopes left empty give the key everything your role can
// grant. The key belongs to the workspace and keeps working if you leave.
//
// An API key cannot create keys: called with one, Create returns a 403 *Error.
// Use a session or an OAuth connection that holds keys:write.
func (k *APIKeys) Create(ctx context.Context, p CreateAPIKeyParams) (*APIKey, error) {
	if err := valid(p); err != nil {
		return nil, err
	}
	return call[APIKey](ctx, k.client, http.MethodPost, "/api-keys", nil, p)
}

// List returns the workspace's API keys. Key is always "": a key's value is
// shown only when it is created.
func (k *APIKeys) List(ctx context.Context) ([]*APIKey, error) {
	return callList[APIKey](ctx, k.client, http.MethodGet, "/api-keys", nil, nil)
}

// Update renames a key, or disables or enables it. It needs the Admin or
// Owner role, for any key in the workspace.
func (k *APIKeys) Update(ctx context.Context, id string, p UpdateAPIKeyParams) (*APIKey, error) {
	seg, err := segment("API key id", id)
	if err != nil {
		return nil, err
	}
	if err := valid(p); err != nil {
		return nil, err
	}
	return call[APIKey](ctx, k.client, http.MethodPatch, "/api-keys/"+seg, nil, p)
}

// Revoke deletes a key. Requests with it fail from then on.
func (k *APIKeys) Revoke(ctx context.Context, id string) error {
	seg, err := segment("API key id", id)
	if err != nil {
		return err
	}
	return send(ctx, k.client, http.MethodDelete, "/api-keys/"+seg, nil)
}
