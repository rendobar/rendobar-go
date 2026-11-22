package rendobar

import (
	"context"
	"net/http"
)

// Orgs manages workspaces (organizations): the one the API key belongs to,
// its settings, members and invitations.
type Orgs struct {
	client *Client
	// Members invites people to the current workspace, changes their roles and removes them.
	Members *Members
	// Invitations manages the current workspace's pending invitations.
	Invitations *Invitations
}

// List returns the workspaces the caller belongs to, with the caller's role in each.
func (o *Orgs) List(ctx context.Context) ([]*Org, error) {
	return callList[Org](ctx, o.client, http.MethodGet, "/orgs", nil, nil)
}

// Current returns the current workspace with the caller's role, its
// subscription, plan and balance.
func (o *Orgs) Current(ctx context.Context) (*CurrentOrg, error) {
	return call[CurrentOrg](ctx, o.client, http.MethodGet, "/orgs/current", nil, nil)
}

// Create makes a workspace with the caller as its owner.
func (o *Orgs) Create(ctx context.Context, p CreateOrgParams) (*Org, error) {
	if err := valid(p); err != nil {
		return nil, err
	}
	return call[Org](ctx, o.client, http.MethodPost, "/orgs", nil, p)
}

// Update renames the current workspace or changes its slug. Fields left "" stay as they are.
func (o *Orgs) Update(ctx context.Context, p UpdateOrgParams) (*Org, error) {
	if err := valid(p); err != nil {
		return nil, err
	}
	return call[Org](ctx, o.client, http.MethodPatch, "/orgs/current", nil, p)
}

// Delete deletes the current workspace and everything in it. Owner only.
func (o *Orgs) Delete(ctx context.Context) error {
	return send(ctx, o.client, http.MethodDelete, "/orgs/current", nil)
}

// GetSettings returns the current workspace's settings.
func (o *Orgs) GetSettings(ctx context.Context) (*OrgSettings, error) {
	return call[OrgSettings](ctx, o.client, http.MethodGet, "/orgs/current/settings", nil, nil)
}

// UpdateSettings changes the settings that are set and returns them all.
// Fields left zero stay as they are.
func (o *Orgs) UpdateSettings(ctx context.Context, p UpdateOrgSettingsParams) (*OrgSettings, error) {
	if err := valid(p); err != nil {
		return nil, err
	}
	return call[OrgSettings](ctx, o.client, http.MethodPatch, "/orgs/current/settings", nil, p)
}

// Members invites people to the current workspace and manages its members.
type Members struct{ client *Client }

// Invite emails an invitation to join the current workspace. Owner or admin only.
func (m *Members) Invite(ctx context.Context, p InviteMemberParams) (*Invitation, error) {
	if err := valid(p); err != nil {
		return nil, err
	}
	return call[Invitation](ctx, m.client, http.MethodPost, "/orgs/current/members", nil, p)
}

// ChangeRole changes a member's role. Owner or admin only.
func (m *Members) ChangeRole(ctx context.Context, memberID string, p ChangeRoleParams) error {
	seg, err := segment("member id", memberID)
	if err != nil {
		return err
	}
	if err := valid(p); err != nil {
		return err
	}
	return send(ctx, m.client, http.MethodPatch, "/orgs/current/members/"+seg, p)
}

// Remove removes a member. Pass your own member id to leave the workspace.
func (m *Members) Remove(ctx context.Context, memberID string) error {
	seg, err := segment("member id", memberID)
	if err != nil {
		return err
	}
	return send(ctx, m.client, http.MethodDelete, "/orgs/current/members/"+seg, nil)
}

// Invitations manages the current workspace's pending invitations.
type Invitations struct{ client *Client }

// Revoke cancels a pending invitation. Owner or admin only.
func (i *Invitations) Revoke(ctx context.Context, invitationID string) error {
	seg, err := segment("invitation id", invitationID)
	if err != nil {
		return err
	}
	return send(ctx, i.client, http.MethodDelete, "/orgs/current/invitations/"+seg, nil)
}
