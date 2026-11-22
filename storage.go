package rendobar

import (
	"context"
	"net/http"
	"net/url"
)

// Storage manages storage connections: buckets you own, connected once under
// an id you choose. A job delivers its output to one through
// CreateParams.Destinations, as "storage://<id>/...". Credentials are tested
// before they are stored and never come back in a response.
type Storage struct{ client *Client }

func storagePath(id string) (string, error) {
	seg, err := segment("storage id", id)
	if err != nil {
		return "", err
	}
	return "/storage/" + seg, nil
}

// Create connects a bucket: one of CreateStorageR2Params,
// CreateStorageS3Params, CreateStorageS3CompatibleParams or
// CreateStorageSupabaseParams. Checks reports the probe that admitted it. A
// failed probe returns a VALIDATION_ERROR *Error, a taken id CONFLICT.
func (s *Storage) Create(ctx context.Context, p CreateStorageParams) (*StorageConnection, error) {
	if p == nil {
		return nil, local(CodeInvalidArgument, "Storage.Create needs params, such as CreateStorageS3Params")
	}
	if err := valid(p); err != nil {
		return nil, err
	}
	return call[StorageConnection](ctx, s.client, http.MethodPost, "/storage", nil, p)
}

// StorageList is every storage connection of the workspace.
type StorageList struct {
	Connections []*StorageConnection
	Total       int64
}

// List returns every storage connection.
func (s *Storage) List(ctx context.Context) (*StorageList, error) {
	items, meta, err := callPage[StorageConnection, struct {
		Total int64 `json:"total"`
	}](ctx, s.client, "/storage", nil)
	if err != nil {
		return nil, err
	}
	return &StorageList{Connections: items, Total: meta.Total}, nil
}

// Get returns one storage connection.
func (s *Storage) Get(ctx context.Context, id string) (*StorageConnection, error) {
	path, err := storagePath(id)
	if err != nil {
		return nil, err
	}
	return call[StorageConnection](ctx, s.client, http.MethodGet, path, nil, nil)
}

// Update replaces a connection's credentials (tested first) or changes its
// public base URL, path template, conflict rule or default. Fields left zero stay as they are.
func (s *Storage) Update(ctx context.Context, id string, p UpdateStorageParams) (*StorageConnection, error) {
	path, err := storagePath(id)
	if err != nil {
		return nil, err
	}
	if err := valid(p); err != nil {
		return nil, err
	}
	return call[StorageConnection](ctx, s.client, http.MethodPatch, path, nil, p)
}

// Delete removes a storage connection. The bucket and its files stay.
func (s *Storage) Delete(ctx context.Context, id string) error {
	path, err := storagePath(id)
	if err != nil {
		return err
	}
	return send(ctx, s.client, http.MethodDelete, path, nil)
}

// Test writes, reads, lists and deletes a probe object. OK reflects the write check.
func (s *Storage) Test(ctx context.Context, id string) (*StorageTestResult, error) {
	path, err := storagePath(id)
	if err != nil {
		return nil, err
	}
	return call[StorageTestResult](ctx, s.client, http.MethodPost, path+"/test", nil, nil)
}

// Stats returns what a connection has been doing: deliveries a day, measured
// throughput, bytes written and the last test's round trip. Never an object name.
func (s *Storage) Stats(ctx context.Context, id string) (*StorageStats, error) {
	path, err := storagePath(id)
	if err != nil {
		return nil, err
	}
	return call[StorageStats](ctx, s.client, http.MethodGet, path+"/stats", nil, nil)
}

// ListObjectsParams picks one page of Storage.ListObjects.
type ListObjectsParams struct {
	// Prefix is the folder to list: "" for the root, else a key ending in "/".
	Prefix string
	// Cursor is the previous page's Cursor, or "" for the first page.
	Cursor string
	// Limit is the page size, up to 1000. 0 is the API's default of 1000.
	Limit int
}

// ListObjects returns one page of the folders and files directly under
// Prefix. Follow Cursor for the next page. A key that cannot list gives a
// VALIDATION_ERROR *Error, a bucket that does not answer UPSTREAM_ERROR.
func (s *Storage) ListObjects(ctx context.Context, id string, p ListObjectsParams) (*StorageObjects, error) {
	path, err := storagePath(id)
	if err != nil {
		return nil, err
	}
	if err := paging("ListObjectsParams.Limit", p.Limit); err != nil {
		return nil, err
	}
	q := url.Values{}
	setString(q, "prefix", p.Prefix)
	setString(q, "cursor", p.Cursor)
	setInt(q, "limit", p.Limit)
	return call[StorageObjects](ctx, s.client, http.MethodGet, path+"/objects", q, nil)
}

// PrepareAWS starts connecting an S3 bucket with one click: a pending
// connection and a CloudFormation quick-create link. The stack reports back
// and the connection finishes on its own: poll Get until Pending is false.
func (s *Storage) PrepareAWS(ctx context.Context, p PrepareAWSStorageParams) (*PreparedAWSStorage, error) {
	if err := valid(p); err != nil {
		return nil, err
	}
	return call[PreparedAWSStorage](ctx, s.client, http.MethodPost, "/storage/aws/prepare", nil, p)
}

// AccountBucketsParams picks the provider Storage.ListAccountBuckets asks.
type AccountBucketsParams struct {
	// Provider is StorageProviderS3 or StorageProviderR2. "" is S3.
	Provider StorageProvider
}

// ListAccountBuckets returns the names of the buckets a connection the
// workspace already has on that provider can see. It is empty when there is
// none, or when its credentials cannot list buckets.
func (s *Storage) ListAccountBuckets(ctx context.Context, p AccountBucketsParams) ([]string, error) {
	q := url.Values{}
	setString(q, "provider", p.Provider)
	res, err := call[struct {
		Buckets []string `json:"buckets"`
	}](ctx, s.client, http.MethodGet, "/storage/buckets", q, nil)
	if err != nil {
		return nil, err
	}
	if res.Buckets == nil {
		return []string{}, nil
	}
	return res.Buckets, nil
}

// LookUpS3Bucket says whether an S3 bucket name is taken, and in which
// region, before anyone has credentials for it. It cannot say whether the
// bucket is yours: S3 names are global.
func (s *Storage) LookUpS3Bucket(ctx context.Context, name string) (*S3BucketLookup, error) {
	if name == "" {
		return nil, local(CodeInvalidArgument, "LookUpS3Bucket needs a bucket name")
	}
	q := url.Values{"name": {name}}
	return call[S3BucketLookup](ctx, s.client, http.MethodGet, "/storage/s3/bucket", q, nil)
}

// StartSupabase starts a Supabase sign-in: send the person to URL. They come
// back to the dashboard with State, which the next calls take.
func (s *Storage) StartSupabase(ctx context.Context) (*SupabaseSignIn, error) {
	return call[SupabaseSignIn](ctx, s.client, http.MethodPost, "/storage/supabase/start", nil, nil)
}

// ListSupabaseProjects returns the projects a Supabase sign-in can see.
func (s *Storage) ListSupabaseProjects(ctx context.Context, state string) (*SupabaseProjects, error) {
	seg, err := segment("Supabase sign-in state", state)
	if err != nil {
		return nil, err
	}
	return call[SupabaseProjects](ctx, s.client, http.MethodGet, "/storage/supabase/sessions/"+seg, nil, nil)
}

// ListSupabaseBuckets returns the buckets one of those projects has.
func (s *Storage) ListSupabaseBuckets(ctx context.Context, state, projectRef string) (*SupabaseBuckets, error) {
	st, err := segment("Supabase sign-in state", state)
	if err != nil {
		return nil, err
	}
	ref, err := segment("Supabase project ref", projectRef)
	if err != nil {
		return nil, err
	}
	return call[SupabaseBuckets](ctx, s.client, http.MethodGet, "/storage/supabase/sessions/"+st+"/projects/"+ref+"/buckets", nil, nil)
}

// FinishSupabase turns a Supabase sign-in into one connection. An expired
// sign-in or a failed probe gives a VALIDATION_ERROR *Error.
func (s *Storage) FinishSupabase(ctx context.Context, p FinishSupabaseParams) (*StorageConnection, error) {
	if err := valid(p); err != nil {
		return nil, err
	}
	return call[StorageConnection](ctx, s.client, http.MethodPost, "/storage/supabase/finish", nil, p)
}
