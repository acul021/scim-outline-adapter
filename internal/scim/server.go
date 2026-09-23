package scim

import (
	"context"
	"crypto/subtle"
	"log/slog"
	"net/http"
	"strings"

	"github.com/acul021/scim-outline-adapter/internal/outline"
)

// OutlineClient is the subset of the Outline API the SCIM server depends on.
// It is an interface so handlers can be tested against a fake without HTTP.
type OutlineClient interface {
	InviteUser(ctx context.Context, email, name, role string) (*outline.User, error)
	GetUser(ctx context.Context, id string) (*outline.User, error)
	FindUserByEmail(ctx context.Context, email string) (*outline.User, error)
	ListUsers(ctx context.Context) ([]outline.User, error)
	UpdateUserName(ctx context.Context, id, name string) (*outline.User, error)
	UpdateUserRole(ctx context.Context, id, role string) (*outline.User, error)
	SuspendUser(ctx context.Context, id string) (*outline.User, error)
	ActivateUser(ctx context.Context, id string) (*outline.User, error)
	DeleteUser(ctx context.Context, id string) error

	CreateGroup(ctx context.Context, name, externalID string) (*outline.Group, error)
	GetGroup(ctx context.Context, id string) (*outline.Group, error)
	UpdateGroup(ctx context.Context, id, name, externalID string) (*outline.Group, error)
	DeleteGroup(ctx context.Context, id string) error
	ListGroups(ctx context.Context) ([]outline.Group, error)
	GroupMemberships(ctx context.Context, groupID string) ([]string, error)
	UserGroups(ctx context.Context, userID string) ([]outline.Group, error)
	AddUserToGroup(ctx context.Context, groupID, userID string) error
	RemoveUserFromGroup(ctx context.Context, groupID, userID string) error
}

// ExternalIDStore persists user externalIds, which Outline cannot store. Users
// without an entry have no externalId; their Outline id stays the only key.
type ExternalIDStore interface {
	Get(outlineID string) string
	Lookup(externalID string) (outlineID string, ok bool)
	Set(outlineID, externalID string) error
	Delete(outlineID string) error
}

// noExternalIDs is the default store: user externalIds are dropped.
type noExternalIDs struct{}

func (noExternalIDs) Get(string) string            { return "" }
func (noExternalIDs) Lookup(string) (string, bool) { return "", false }
func (noExternalIDs) Set(string, string) error     { return nil }
func (noExternalIDs) Delete(string) error          { return nil }

// Server holds the SCIM server dependencies.
type Server struct {
	client     OutlineClient
	roles      RoleMap
	token      string
	hardDelete bool
	extIDs     ExternalIDStore
}

// NewServer builds a SCIM server. User externalIds are not stored unless
// WithExternalIDStore is called.
func NewServer(client OutlineClient, roles RoleMap, token string, hardDelete bool) *Server {
	return &Server{client: client, roles: roles, token: token, hardDelete: hardDelete, extIDs: noExternalIDs{}}
}

// WithExternalIDStore enables the user externalId mapping.
func (s *Server) WithExternalIDStore(st ExternalIDStore) *Server {
	s.extIDs = st
	return s
}

// Handler returns the authenticated SCIM router mounted under /scim/v2.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /scim/v2/ServiceProviderConfig", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, serviceProviderConfig())
	})

	mux.HandleFunc("POST /scim/v2/Users", s.createUser)
	mux.HandleFunc("GET /scim/v2/Users", s.listUsers)
	mux.HandleFunc("GET /scim/v2/Users/{id}", s.getUser)
	mux.HandleFunc("PUT /scim/v2/Users/{id}", s.putUser)
	mux.HandleFunc("PATCH /scim/v2/Users/{id}", s.patchUser)
	mux.HandleFunc("DELETE /scim/v2/Users/{id}", s.deleteUser)

	mux.HandleFunc("POST /scim/v2/Groups", s.createGroup)
	mux.HandleFunc("GET /scim/v2/Groups", s.listGroups)
	mux.HandleFunc("GET /scim/v2/Groups/{id}", s.getGroup)
	mux.HandleFunc("PUT /scim/v2/Groups/{id}", s.putGroup)
	mux.HandleFunc("PATCH /scim/v2/Groups/{id}", s.patchGroup)
	mux.HandleFunc("DELETE /scim/v2/Groups/{id}", s.deleteGroup)

	return s.authMiddleware(mux)
}

// authMiddleware enforces the static bearer token with a constant-time compare
// so token verification does not leak length or content via timing.
func (s *Server) authMiddleware(next http.Handler) http.Handler {
	want := []byte(s.token)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
		if subtle.ConstantTimeCompare([]byte(got), want) != 1 {
			writeError(w, http.StatusUnauthorized, "", "invalid or missing bearer token")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// location builds the absolute resource URL for a meta.location field from the
// incoming request, so the value is correct behind whatever host/proxy is used.
func location(r *http.Request, resource, id string) string {
	scheme := "https"
	if r.TLS == nil && r.Header.Get("X-Forwarded-Proto") == "" {
		scheme = "http"
	}
	if p := r.Header.Get("X-Forwarded-Proto"); p != "" {
		scheme = p
	}
	return scheme + "://" + r.Host + "/scim/v2/" + resource + "/" + id
}

// recomputeRole recalculates and, if changed, applies a user's Outline team role
// from their current group memberships. Called only for users affected by a
// membership change so unrelated users are never touched.
func (s *Server) recomputeRole(ctx context.Context, userID string) error {
	groups, err := s.client.UserGroups(ctx, userID)
	if err != nil {
		return err
	}
	names := make([]string, 0, len(groups))
	for _, g := range groups {
		names = append(names, g.Name)
	}
	target := s.roles.DeriveRole(names)

	u, err := s.client.GetUser(ctx, userID)
	if err != nil {
		return err
	}
	// Outline rejects users.update_role on a suspended account (403). A suspended
	// user has no access anyway, so their team role is moot; it will be
	// recomputed on the next membership change after they are reactivated.
	if u.IsSuspended {
		return nil
	}
	if u.Role == target {
		return nil
	}
	if _, err := s.client.UpdateUserRole(ctx, userID, target); err != nil {
		return err
	}
	slog.Info("role recomputed", "user", userID, "from", u.Role, "to", target)
	return nil
}
