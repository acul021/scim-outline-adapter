// Package outline is a thin client for Outline's RPC-style REST API. Outline
// exposes every operation as POST /api/<method> with a JSON body and a bearer
// token; there are no path parameters or verbs beyond POST, so the client is a
// set of typed wrappers over a single request helper.
package outline

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// maxRetries bounds how many times a request is retried after a 429. Outline
// returns Retry-After; we honour it but cap total attempts so a hung upstream
// can never stall a SCIM request indefinitely.
const maxRetries = 2

// ErrNotFound is returned when Outline reports a resource does not exist. Handlers
// translate it into a SCIM 404 rather than a 500.
var ErrNotFound = errors.New("outline: resource not found")

// User is the subset of Outline's user object the adapter needs. Outline has no
// structured given/family name, only a single display name.
type User struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Email       string `json:"email"`
	Role        string `json:"role"`
	IsSuspended bool   `json:"isSuspended"`
}

// Group mirrors Outline's group object. Outline stores externalId on groups
// (verified against the live API), so it round-trips through SCIM.
type Group struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	ExternalID string `json:"externalId"`
}

// Client talks to a single Outline instance with one API token.
type Client struct {
	baseURL string
	token   string
	http    *http.Client

	// SuppressInviteEmails asks Outline not to mail an invite for users created
	// via users.invite (Outline's suppressEmail flag).
	SuppressInviteEmails bool
}

// New builds a Client. baseURL is the instance root (without /api); token is an
// Outline API key with admin rights.
func New(baseURL, token string) *Client {
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		token:   token,
		http:    &http.Client{Timeout: 30 * time.Second},
	}
}

// apiError carries the fields Outline returns on failures so we can distinguish
// not-found from other errors.
type apiError struct {
	Status  int    `json:"status"`
	Error   string `json:"error"`
	Message string `json:"message"`
}

// post sends one RPC call. out may be nil when the caller ignores the body. It
// retries on 429 up to maxRetries, honouring Retry-After.
func (c *Client) post(ctx context.Context, method string, body any, out any) error {
	payload, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("marshal %s: %w", method, err)
	}

	for attempt := 0; ; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/api/"+method, bytes.NewReader(payload))
		if err != nil {
			return fmt.Errorf("build %s: %w", method, err)
		}
		req.Header.Set("Authorization", "Bearer "+c.token)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json")

		resp, err := c.http.Do(req)
		if err != nil {
			return fmt.Errorf("call %s: %w", method, err)
		}
		data, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			return fmt.Errorf("read %s: %w", method, err)
		}

		if resp.StatusCode == http.StatusTooManyRequests && attempt < maxRetries {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(retryAfter(resp.Header.Get("Retry-After"))):
			}
			continue
		}

		if resp.StatusCode >= 400 {
			var ae apiError
			_ = json.Unmarshal(data, &ae)
			if resp.StatusCode == http.StatusNotFound || ae.Error == "not_found" {
				return ErrNotFound
			}
			msg := ae.Message
			if msg == "" {
				msg = strings.TrimSpace(string(data))
			}
			return fmt.Errorf("outline %s: %d %s", method, resp.StatusCode, msg)
		}

		if out != nil {
			if err := json.Unmarshal(data, out); err != nil {
				return fmt.Errorf("decode %s: %w", method, err)
			}
		}
		return nil
	}
}

// retryAfter parses a Retry-After header (seconds) into a bounded delay.
func retryAfter(h string) time.Duration {
	if secs, err := strconv.Atoi(strings.TrimSpace(h)); err == nil && secs > 0 {
		if secs > 30 {
			secs = 30
		}
		return time.Duration(secs) * time.Second
	}
	return time.Second
}

// InviteUser creates an Outline account. Outline returns the created user in
// data.users; on a duplicate email it returns an empty list (the invite is
// silently dropped), so callers must fall back to FindUserByEmail for adoption.
func (c *Client) InviteUser(ctx context.Context, email, name, role string) (*User, error) {
	var resp struct {
		Data struct {
			Users []User `json:"users"`
		} `json:"data"`
	}
	body := map[string]any{
		"invites":       []map[string]string{{"email": email, "name": name, "role": role}},
		"suppressEmail": c.SuppressInviteEmails,
	}
	if err := c.post(ctx, "users.invite", body, &resp); err != nil {
		return nil, err
	}
	if len(resp.Data.Users) == 0 {
		return nil, nil
	}
	return &resp.Data.Users[0], nil
}

// GetUser fetches one user by Outline UUID.
func (c *Client) GetUser(ctx context.Context, id string) (*User, error) {
	var resp struct {
		Data User `json:"data"`
	}
	if err := c.post(ctx, "users.info", map[string]string{"id": id}, &resp); err != nil {
		return nil, err
	}
	return &resp.Data, nil
}

// FindUserByEmail returns the user with an exact email match, or nil. filter=all
// is required because users.list hides suspended users by default.
func (c *Client) FindUserByEmail(ctx context.Context, email string) (*User, error) {
	var resp struct {
		Data []User `json:"data"`
	}
	body := map[string]any{"emails": []string{email}, "filter": "all"}
	if err := c.post(ctx, "users.list", body, &resp); err != nil {
		return nil, err
	}
	for i := range resp.Data {
		if strings.EqualFold(resp.Data[i].Email, email) {
			return &resp.Data[i], nil
		}
	}
	return nil, nil
}

// ListUsers returns every user (including suspended), paging through Outline's
// offset pagination. Used for unfiltered SCIM /Users list requests.
func (c *Client) ListUsers(ctx context.Context) ([]User, error) {
	var all []User
	offset := 0
	for {
		var resp struct {
			Pagination struct {
				Total int `json:"total"`
			} `json:"pagination"`
			Data []User `json:"data"`
		}
		body := map[string]any{"filter": "all", "limit": 100, "offset": offset}
		if err := c.post(ctx, "users.list", body, &resp); err != nil {
			return nil, err
		}
		all = append(all, resp.Data...)
		offset += len(resp.Data)
		if len(resp.Data) == 0 || offset >= resp.Pagination.Total {
			return all, nil
		}
	}
}

// UpdateUserName renames a user (admin editing another account).
func (c *Client) UpdateUserName(ctx context.Context, id, name string) (*User, error) {
	var resp struct {
		Data User `json:"data"`
	}
	if err := c.post(ctx, "users.update", map[string]string{"id": id, "name": name}, &resp); err != nil {
		return nil, err
	}
	return &resp.Data, nil
}

// UpdateUserRole sets the Outline team role. role must be one of the values in
// ValidRoles; Outline rejects anything else with a validation error.
func (c *Client) UpdateUserRole(ctx context.Context, id, role string) (*User, error) {
	var resp struct {
		Data User `json:"data"`
	}
	if err := c.post(ctx, "users.update_role", map[string]string{"id": id, "role": role}, &resp); err != nil {
		return nil, err
	}
	return &resp.Data, nil
}

// SuspendUser deactivates a user (SCIM active=false / soft delete).
func (c *Client) SuspendUser(ctx context.Context, id string) (*User, error) {
	var resp struct {
		Data User `json:"data"`
	}
	if err := c.post(ctx, "users.suspend", map[string]string{"id": id}, &resp); err != nil {
		return nil, err
	}
	return &resp.Data, nil
}

// ActivateUser reactivates a suspended user (SCIM active=true).
func (c *Client) ActivateUser(ctx context.Context, id string) (*User, error) {
	var resp struct {
		Data User `json:"data"`
	}
	if err := c.post(ctx, "users.activate", map[string]string{"id": id}, &resp); err != nil {
		return nil, err
	}
	return &resp.Data, nil
}

// DeleteUser permanently removes a user (SCIM DELETE when HARD_DELETE_USERS).
func (c *Client) DeleteUser(ctx context.Context, id string) error {
	return c.post(ctx, "users.delete", map[string]string{"id": id}, nil)
}

// CreateGroup creates a group. externalId is optional and stored by Outline.
func (c *Client) CreateGroup(ctx context.Context, name, externalID string) (*Group, error) {
	var resp struct {
		Data Group `json:"data"`
	}
	body := map[string]string{"name": name}
	if externalID != "" {
		body["externalId"] = externalID
	}
	if err := c.post(ctx, "groups.create", body, &resp); err != nil {
		return nil, err
	}
	return &resp.Data, nil
}

// GetGroup fetches one group by UUID.
func (c *Client) GetGroup(ctx context.Context, id string) (*Group, error) {
	var resp struct {
		Data Group `json:"data"`
	}
	if err := c.post(ctx, "groups.info", map[string]string{"id": id}, &resp); err != nil {
		return nil, err
	}
	return &resp.Data, nil
}

// UpdateGroup renames a group and/or updates its externalId. Empty fields are
// omitted so a rename does not clobber an existing externalId.
func (c *Client) UpdateGroup(ctx context.Context, id, name, externalID string) (*Group, error) {
	var resp struct {
		Data Group `json:"data"`
	}
	body := map[string]string{"id": id}
	if name != "" {
		body["name"] = name
	}
	if externalID != "" {
		body["externalId"] = externalID
	}
	if err := c.post(ctx, "groups.update", body, &resp); err != nil {
		return nil, err
	}
	return &resp.Data, nil
}

// DeleteGroup removes a group. Membership is dropped by Outline; callers must
// recompute affected users' roles beforehand.
func (c *Client) DeleteGroup(ctx context.Context, id string) error {
	return c.post(ctx, "groups.delete", map[string]string{"id": id}, nil)
}

// ListGroups returns every group, paging through Outline's offset pagination.
func (c *Client) ListGroups(ctx context.Context) ([]Group, error) {
	var all []Group
	offset := 0
	for {
		var resp struct {
			Pagination struct {
				Total int `json:"total"`
			} `json:"pagination"`
			Data struct {
				Groups []Group `json:"groups"`
			} `json:"data"`
		}
		if err := c.post(ctx, "groups.list", map[string]any{"limit": 100, "offset": offset}, &resp); err != nil {
			return nil, err
		}
		all = append(all, resp.Data.Groups...)
		offset += len(resp.Data.Groups)
		if len(resp.Data.Groups) == 0 || offset >= resp.Pagination.Total {
			return all, nil
		}
	}
}

// GroupMemberships returns the Outline user IDs that belong to a group.
func (c *Client) GroupMemberships(ctx context.Context, groupID string) ([]string, error) {
	var ids []string
	offset := 0
	for {
		var resp struct {
			Pagination struct {
				Total int `json:"total"`
			} `json:"pagination"`
			Data struct {
				GroupMemberships []struct {
					UserID string `json:"userId"`
				} `json:"groupMemberships"`
			} `json:"data"`
		}
		body := map[string]any{"id": groupID, "limit": 100, "offset": offset}
		if err := c.post(ctx, "groups.memberships", body, &resp); err != nil {
			return nil, err
		}
		for _, m := range resp.Data.GroupMemberships {
			ids = append(ids, m.UserID)
		}
		offset += len(resp.Data.GroupMemberships)
		if len(resp.Data.GroupMemberships) == 0 || offset >= resp.Pagination.Total {
			return ids, nil
		}
	}
}

// UserGroups returns the groups a user belongs to. groups.list accepts a userId
// filter, giving the reverse lookup in one call (there is no users.memberships
// endpoint). This is the cheapest correct source for role derivation.
func (c *Client) UserGroups(ctx context.Context, userID string) ([]Group, error) {
	var all []Group
	offset := 0
	for {
		var resp struct {
			Pagination struct {
				Total int `json:"total"`
			} `json:"pagination"`
			Data struct {
				Groups []Group `json:"groups"`
			} `json:"data"`
		}
		body := map[string]any{"userId": userID, "limit": 100, "offset": offset}
		if err := c.post(ctx, "groups.list", body, &resp); err != nil {
			return nil, err
		}
		all = append(all, resp.Data.Groups...)
		offset += len(resp.Data.Groups)
		if len(resp.Data.Groups) == 0 || offset >= resp.Pagination.Total {
			return all, nil
		}
	}
}

// AddUserToGroup adds a member. Idempotent on Outline's side.
func (c *Client) AddUserToGroup(ctx context.Context, groupID, userID string) error {
	return c.post(ctx, "groups.add_user", map[string]string{"id": groupID, "userId": userID}, nil)
}

// RemoveUserFromGroup removes a member.
func (c *Client) RemoveUserFromGroup(ctx context.Context, groupID, userID string) error {
	return c.post(ctx, "groups.remove_user", map[string]string{"id": groupID, "userId": userID}, nil)
}
