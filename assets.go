package rendobar

import (
	"context"
	"net/http"
	"net/url"
)

// Assets lists, reads and deletes the workspace's files: uploads, fetched
// URLs and job outputs. Uploads.Create adds one.
type Assets struct{ client *Client }

// ListAssetsParams filters Assets.List. The zero value lists every asset.
type ListAssetsParams struct {
	Kind      AssetKind      // such as AssetKindMedia, or "" for all
	Scope     AssetScope     // AssetScopeOrg for the workspace's own, or "" for all
	Status    AssetStatus    // such as AssetStatusReady, or "" for all
	Lifecycle AssetLifecycle // AssetLifecycleEphemeral or AssetLifecyclePersisted, or "" for both
	// Limit is the page size. 0 is the API's default.
	Limit int
	// Offset skips that many matching assets.
	Offset int
}

// AssetPage is one page of assets.
type AssetPage struct {
	Assets []*Asset
	Meta   OffsetMeta
}

// List returns one page of assets. A negative Limit or Offset is an INVALID_ARGUMENT *Error.
func (a *Assets) List(ctx context.Context, p ListAssetsParams) (*AssetPage, error) {
	if err := paging("ListAssetsParams Limit and Offset", p.Limit, p.Offset); err != nil {
		return nil, err
	}
	q := url.Values{}
	setString(q, "kind", p.Kind)
	setString(q, "scope", p.Scope)
	setString(q, "status", p.Status)
	setString(q, "lifecycle", p.Lifecycle)
	setInt(q, "limit", p.Limit)
	setInt(q, "offset", p.Offset)
	return a.page(ctx, "/assets", q)
}

// TemplatesParams pages Assets.Templates.
type TemplatesParams struct {
	Limit  int // the page size, or 0 for the API's default
	Offset int // how many templates to skip
}

// Templates returns one page of the templates Rendobar provides to every workspace.
func (a *Assets) Templates(ctx context.Context, p TemplatesParams) (*AssetPage, error) {
	if err := paging("TemplatesParams Limit and Offset", p.Limit, p.Offset); err != nil {
		return nil, err
	}
	q := url.Values{}
	setInt(q, "limit", p.Limit)
	setInt(q, "offset", p.Offset)
	return a.page(ctx, "/assets/templates", q)
}

func (a *Assets) page(ctx context.Context, path string, q url.Values) (*AssetPage, error) {
	items, meta, err := callPage[Asset, OffsetMeta](ctx, a.client, path, q)
	if err != nil {
		return nil, err
	}
	return &AssetPage{Assets: items, Meta: meta}, nil
}

// Get returns one asset.
func (a *Assets) Get(ctx context.Context, id string) (*Asset, error) {
	seg, err := segment("asset id", id)
	if err != nil {
		return nil, err
	}
	return call[Asset](ctx, a.client, http.MethodGet, "/assets/"+seg, nil, nil)
}

// Delete deletes an asset and its file.
func (a *Assets) Delete(ctx context.Context, id string) error {
	seg, err := segment("asset id", id)
	if err != nil {
		return err
	}
	return send(ctx, a.client, http.MethodDelete, "/assets/"+seg, nil)
}

// Download returns a short-lived URL that downloads the asset's file.
func (a *Assets) Download(ctx context.Context, id string) (*AssetDownload, error) {
	seg, err := segment("asset id", id)
	if err != nil {
		return nil, err
	}
	return call[AssetDownload](ctx, a.client, http.MethodGet, "/assets/"+seg+"/download", nil, nil)
}
