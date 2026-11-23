package gateway

import (
	"context"

	"github.com/0xfakeSpike/ollupi/account"
)

// userList names every profile of the home and the one this connection is.
func (c *conn) userList(ctx context.Context, _ Empty) (UserListResult, error) {
	users, err := c.s.app.Users(ctx)
	out := make([]UserRow, len(users))
	for i, u := range users {
		out[i] = userRow(u)
	}
	return UserListResult{Data: out, Current: c.user}, err
}

func (c *conn) userCreate(ctx context.Context, p UserCreateParams) (UserResult, error) {
	u, err := c.s.app.CreateUser(ctx, p.Name, p.Color)
	return UserResult{User: userRow(u)}, err
}

func (c *conn) userRename(ctx context.Context, p UserRenameParams) (UserResult, error) {
	u, err := c.s.app.RenameUser(ctx, p.UserID, p.Name)
	return UserResult{User: userRow(u)}, err
}

func (c *conn) userColor(ctx context.Context, p UserColorParams) (UserResult, error) {
	u, err := c.s.app.SetUserColor(ctx, p.UserID, p.Color)
	return UserResult{User: userRow(u)}, err
}

func userRow(u account.User) UserRow {
	return UserRow{ID: u.ID, Name: u.Name, Color: u.Color, Created: u.Created}
}
