package scim

import (
	"context"
	"fmt"
	"strings"

	"github.com/acul021/scim-outline-adapter/internal/outline"
)

// fakeClient is an in-memory OutlineClient for handler tests. It models the
// behaviours verified against the live Outline API: invite returns nil on a
// duplicate email, memberships drive role derivation, and externalId is stored
// on groups only.
type fakeClient struct {
	users   map[string]*outline.User
	groups  map[string]*outline.Group
	members map[string]map[string]bool // groupID -> set of userID
	seq     int

	// roleUpdateForbiddenIfSuspended mirrors Outline returning 403 on
	// users.update_role for a suspended account.
	roleUpdateForbiddenIfSuspended bool
}

// nextID returns a deterministic unique id, avoiding an external uuid dependency
// in this dependency-free module.
func (f *fakeClient) nextID(prefix string) string {
	f.seq++
	return fmt.Sprintf("%s-%08d", prefix, f.seq)
}

func newFake() *fakeClient {
	return &fakeClient{
		users:   map[string]*outline.User{},
		groups:  map[string]*outline.Group{},
		members: map[string]map[string]bool{},
	}
}

func (f *fakeClient) InviteUser(_ context.Context, email, name, role string) (*outline.User, error) {
	for _, u := range f.users {
		if strings.EqualFold(u.Email, email) {
			return nil, nil // duplicate: Outline silently drops the invite
		}
	}
	u := &outline.User{ID: f.nextID("usr"), Email: email, Name: name, Role: role}
	f.users[u.ID] = u
	return u, nil
}

func (f *fakeClient) GetUser(_ context.Context, id string) (*outline.User, error) {
	u, ok := f.users[id]
	if !ok {
		return nil, outline.ErrNotFound
	}
	cp := *u
	return &cp, nil
}

func (f *fakeClient) FindUserByEmail(_ context.Context, email string) (*outline.User, error) {
	for _, u := range f.users {
		if strings.EqualFold(u.Email, email) {
			cp := *u
			return &cp, nil
		}
	}
	return nil, nil
}

func (f *fakeClient) ListUsers(_ context.Context) ([]outline.User, error) {
	out := make([]outline.User, 0, len(f.users))
	for _, u := range f.users {
		out = append(out, *u)
	}
	return out, nil
}

func (f *fakeClient) UpdateUserName(_ context.Context, id, name string) (*outline.User, error) {
	u, ok := f.users[id]
	if !ok {
		return nil, outline.ErrNotFound
	}
	u.Name = name
	cp := *u
	return &cp, nil
}

func (f *fakeClient) UpdateUserRole(_ context.Context, id, role string) (*outline.User, error) {
	u, ok := f.users[id]
	if !ok {
		return nil, outline.ErrNotFound
	}
	if f.roleUpdateForbiddenIfSuspended && u.IsSuspended {
		return nil, fmt.Errorf("outline users.update_role: 403 Authorization error")
	}
	u.Role = role
	cp := *u
	return &cp, nil
}

func (f *fakeClient) SuspendUser(_ context.Context, id string) (*outline.User, error) {
	u, ok := f.users[id]
	if !ok {
		return nil, outline.ErrNotFound
	}
	u.IsSuspended = true
	cp := *u
	return &cp, nil
}

func (f *fakeClient) ActivateUser(_ context.Context, id string) (*outline.User, error) {
	u, ok := f.users[id]
	if !ok {
		return nil, outline.ErrNotFound
	}
	u.IsSuspended = false
	cp := *u
	return &cp, nil
}

func (f *fakeClient) DeleteUser(_ context.Context, id string) error {
	if _, ok := f.users[id]; !ok {
		return outline.ErrNotFound
	}
	delete(f.users, id)
	for _, set := range f.members {
		delete(set, id)
	}
	return nil
}

func (f *fakeClient) CreateGroup(_ context.Context, name, externalID string) (*outline.Group, error) {
	g := &outline.Group{ID: f.nextID("grp"), Name: name, ExternalID: externalID}
	f.groups[g.ID] = g
	f.members[g.ID] = map[string]bool{}
	return g, nil
}

func (f *fakeClient) GetGroup(_ context.Context, id string) (*outline.Group, error) {
	g, ok := f.groups[id]
	if !ok {
		return nil, outline.ErrNotFound
	}
	cp := *g
	return &cp, nil
}

func (f *fakeClient) UpdateGroup(_ context.Context, id, name, externalID string) (*outline.Group, error) {
	g, ok := f.groups[id]
	if !ok {
		return nil, outline.ErrNotFound
	}
	if name != "" {
		g.Name = name
	}
	if externalID != "" {
		g.ExternalID = externalID
	}
	cp := *g
	return &cp, nil
}

func (f *fakeClient) DeleteGroup(_ context.Context, id string) error {
	if _, ok := f.groups[id]; !ok {
		return outline.ErrNotFound
	}
	delete(f.groups, id)
	delete(f.members, id)
	return nil
}

func (f *fakeClient) ListGroups(_ context.Context) ([]outline.Group, error) {
	out := make([]outline.Group, 0, len(f.groups))
	for _, g := range f.groups {
		out = append(out, *g)
	}
	return out, nil
}

func (f *fakeClient) GroupMemberships(_ context.Context, groupID string) ([]string, error) {
	if _, ok := f.groups[groupID]; !ok {
		return nil, outline.ErrNotFound
	}
	var ids []string
	for uid := range f.members[groupID] {
		ids = append(ids, uid)
	}
	return ids, nil
}

func (f *fakeClient) UserGroups(_ context.Context, userID string) ([]outline.Group, error) {
	var out []outline.Group
	for gid, set := range f.members {
		if set[userID] {
			out = append(out, *f.groups[gid])
		}
	}
	return out, nil
}

func (f *fakeClient) AddUserToGroup(_ context.Context, groupID, userID string) error {
	if _, ok := f.groups[groupID]; !ok {
		return outline.ErrNotFound
	}
	f.members[groupID][userID] = true
	return nil
}

func (f *fakeClient) RemoveUserFromGroup(_ context.Context, groupID, userID string) error {
	if _, ok := f.groups[groupID]; !ok {
		return outline.ErrNotFound
	}
	delete(f.members[groupID], userID)
	return nil
}
