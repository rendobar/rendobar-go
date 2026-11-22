package rendobar

import (
	"context"
	"net/http"
)

// Notifications reads the workspace's notifications and marks them read.
type Notifications struct{ client *Client }

// NotificationList is the newest notifications, with the caller's unread count.
type NotificationList struct {
	Notifications []*Notification
	// Unread counts the caller's unread notifications, which may be more than this list holds.
	Unread int64
}

// List returns the newest 50 notifications, each with the caller's read state.
func (n *Notifications) List(ctx context.Context) (*NotificationList, error) {
	items, meta, err := callPage[Notification, struct {
		Unread int64 `json:"unread"`
	}](ctx, n.client, "/notifications", nil)
	if err != nil {
		return nil, err
	}
	return &NotificationList{Notifications: items, Unread: meta.Unread}, nil
}

// MarkRead marks notifications read for the caller. Unknown ids are ignored.
func (n *Notifications) MarkRead(ctx context.Context, p MarkReadParams) (*NotificationsRead, error) {
	if err := valid(p); err != nil {
		return nil, err
	}
	return call[NotificationsRead](ctx, n.client, http.MethodPost, "/notifications/read", nil, p)
}
