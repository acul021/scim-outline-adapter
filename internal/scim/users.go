package scim

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/acul021/scim-outline-adapter/internal/outline"
)

// decodeUser reads a User body and separately reports whether `active` was
// present, since a JSON bool cannot distinguish an omitted field from false and
// SCIM defaults active to true on create.
func decodeUser(r *http.Request) (User, bool, error) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return User{}, false, err
	}
	var u User
	if err := json.Unmarshal(body, &u); err != nil {
		return User{}, false, err
	}
	var probe struct {
		Active *bool `json:"active"`
	}
	_ = json.Unmarshal(body, &probe)
	if probe.Active != nil {
		u.Active = *probe.Active
	} else {
		u.Active = true // SCIM default
	}
	return u, probe.Active != nil, nil
}

// toSCIMUser maps an Outline user to a SCIM user resource. Outline does not
// store externalId for users; it comes from the external id store and is
// omitted when the store has no entry (authentik matches on id anyway).
func (s *Server) toSCIMUser(r *http.Request, u *outline.User) User {
	return User{
		Schemas:    []string{schemaUser},
		ID:         u.ID,
		ExternalID: s.extIDs.Get(u.ID),
		UserName:   u.Email,
		Name:       splitName(u.Name),
		Emails:     []Email{{Value: u.Email, Primary: true, Type: "work"}},
		Active:     !u.IsSuspended,
		Meta:       &Meta{ResourceType: "User", Location: location(r, "Users", u.ID)},
	}
}

// primaryEmail returns the resource's login email: the primary (or first)
// emails entry. userName is deliberately NOT a fallback — authentik's default
// SCIM mapping sets it to the username, not an address, and inviting a
// non-address makes Outline reject the create; better to fail loudly here.
func (u *User) primaryEmail() string {
	for _, e := range u.Emails {
		if e.Primary && e.Value != "" {
			return e.Value
		}
	}
	if len(u.Emails) > 0 && u.Emails[0].Value != "" {
		return u.Emails[0].Value
	}
	return ""
}

func (s *Server) createUser(w http.ResponseWriter, r *http.Request) {
	in, activeSet, err := decodeUser(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalidSyntax", "malformed request body")
		return
	}
	email := in.primaryEmail()
	if email == "" {
		writeError(w, http.StatusBadRequest, "invalidValue", "an emails entry with an address is required")
		return
	}
	name := joinName(in.Name)
	if name == "" {
		name = email
	}

	ctx := r.Context()
	// New users hold no group memberships yet, so they start at the default role;
	// group PATCH ops that follow will promote them via role derivation.
	created, err := s.client.InviteUser(ctx, email, name, roleViewer)
	if err != nil {
		s.fail(w, err)
		return
	}

	status := http.StatusCreated
	if created == nil {
		// Duplicate email: adopt the existing account for idempotency.
		created, err = s.client.FindUserByEmail(ctx, email)
		if err != nil {
			s.fail(w, err)
			return
		}
		if created == nil {
			writeError(w, http.StatusConflict, "uniqueness", "user exists but could not be resolved")
			return
		}
		status = http.StatusOK
		if name != "" && name != created.Name {
			if updated, uerr := s.client.UpdateUserName(ctx, created.ID, name); uerr == nil {
				created = updated
			}
		}
	}

	// active=false explicitly set on create means provision-then-disable.
	if activeSet && !in.Active && !created.IsSuspended {
		if suspended, serr := s.client.SuspendUser(ctx, created.ID); serr == nil {
			created = suspended
		} else {
			s.fail(w, serr)
			return
		}
	}

	if !s.storeExternalID(w, created.ID, in.ExternalID) {
		return
	}
	writeJSON(w, status, s.toSCIMUser(r, created))
}

func (s *Server) getUser(w http.ResponseWriter, r *http.Request) {
	u, err := s.client.GetUser(r.Context(), r.PathValue("id"))
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, s.toSCIMUser(r, u))
}

func (s *Server) listUsers(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	f, err := parseFilter(r.URL.Query().Get("filter"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalidFilter", err.Error())
		return
	}

	var users []outline.User
	if f != nil {
		switch f.attr {
		case "username", "emails.value", "emails":
			if u, ferr := s.client.FindUserByEmail(ctx, f.value); ferr != nil {
				s.fail(w, ferr)
				return
			} else if u != nil {
				users = []outline.User{*u}
			}
		case "externalid":
			// Resolved through the external id store. Without a mapping the
			// result is empty and the client falls through to create, which
			// adopts by email.
			if id, ok := s.extIDs.Lookup(f.value); ok {
				u, gerr := s.client.GetUser(ctx, id)
				switch {
				case errors.Is(gerr, outline.ErrNotFound):
					// Deleted in Outline behind our back: drop the stale entry.
					_ = s.extIDs.Delete(id)
				case gerr != nil:
					s.fail(w, gerr)
					return
				default:
					users = []outline.User{*u}
				}
			}
		default:
			writeError(w, http.StatusBadRequest, "invalidFilter", "unsupported user filter attribute: "+f.attr)
			return
		}
	} else {
		if users, err = s.client.ListUsers(ctx); err != nil {
			s.fail(w, err)
			return
		}
	}

	start, count := pageParams(r)
	resources := make([]any, 0)
	total := len(users)
	for i := start - 1; i < total && len(resources) < count; i++ {
		resources = append(resources, s.toSCIMUser(r, &users[i]))
	}
	writeJSON(w, http.StatusOK, ListResponse{
		Schemas:      []string{schemaListResp},
		TotalResults: total,
		StartIndex:   start,
		ItemsPerPage: len(resources),
		Resources:    resources,
	})
}

func (s *Server) putUser(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	in, _, err := decodeUser(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalidSyntax", "malformed request body")
		return
	}
	ctx := r.Context()
	cur, err := s.client.GetUser(ctx, id)
	if err != nil {
		s.fail(w, err)
		return
	}

	if name := joinName(in.Name); name != "" && name != cur.Name {
		if cur, err = s.client.UpdateUserName(ctx, id, name); err != nil {
			s.fail(w, err)
			return
		}
	}
	if in.Active && cur.IsSuspended {
		if cur, err = s.client.ActivateUser(ctx, id); err != nil {
			s.fail(w, err)
			return
		}
	} else if !in.Active && !cur.IsSuspended {
		if cur, err = s.client.SuspendUser(ctx, id); err != nil {
			s.fail(w, err)
			return
		}
	}
	if !s.storeExternalID(w, id, in.ExternalID) {
		return
	}
	writeJSON(w, http.StatusOK, s.toSCIMUser(r, cur))
}

func (s *Server) patchUser(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req patchRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalidSyntax", "malformed request body")
		return
	}
	res, err := applyUserPatch(req.Operations)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalidValue", err.Error())
		return
	}
	ctx := r.Context()
	cur, err := s.client.GetUser(ctx, id)
	if err != nil {
		s.fail(w, err)
		return
	}
	if res.setName != nil && *res.setName != "" && *res.setName != cur.Name {
		if cur, err = s.client.UpdateUserName(ctx, id, *res.setName); err != nil {
			s.fail(w, err)
			return
		}
	}
	if res.setActive != nil {
		if *res.setActive && cur.IsSuspended {
			if cur, err = s.client.ActivateUser(ctx, id); err != nil {
				s.fail(w, err)
				return
			}
		} else if !*res.setActive && !cur.IsSuspended {
			if cur, err = s.client.SuspendUser(ctx, id); err != nil {
				s.fail(w, err)
				return
			}
		}
	}
	if res.setExternalID != nil && !s.storeExternalID(w, id, *res.setExternalID) {
		return
	}
	writeJSON(w, http.StatusOK, s.toSCIMUser(r, cur))
}

func (s *Server) deleteUser(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	ctx := r.Context()
	var err error
	if s.hardDelete {
		err = s.client.DeleteUser(ctx, id)
	} else {
		_, err = s.client.SuspendUser(ctx, id)
	}
	if err != nil {
		s.fail(w, err)
		return
	}
	if s.hardDelete {
		// The account is gone, so its externalId must not resolve anymore. A
		// suspended user keeps the entry so reactivation finds it again.
		if err := s.extIDs.Delete(id); err != nil {
			slog.Error("external id store delete failed", "user", id, "err", err)
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

// storeExternalID records externalID for an Outline user. An empty externalID
// leaves any existing entry alone, since PUT bodies from some clients omit it.
// It writes an error response and returns false if the store fails.
func (s *Server) storeExternalID(w http.ResponseWriter, outlineID, externalID string) bool {
	if externalID == "" {
		return true
	}
	if err := s.extIDs.Set(outlineID, externalID); err != nil {
		slog.Error("external id store write failed", "user", outlineID, "err", err)
		writeError(w, http.StatusInternalServerError, "", "could not persist externalId")
		return false
	}
	return true
}

// pageParams parses SCIM startIndex (1-based) and count query parameters,
// defaulting to the whole result set when absent.
func pageParams(r *http.Request) (start, count int) {
	start = 1
	count = 1 << 30
	q := r.URL.Query()
	if v, err := strconv.Atoi(q.Get("startIndex")); err == nil && v >= 1 {
		start = v
	}
	if v, err := strconv.Atoi(q.Get("count")); err == nil && v >= 0 {
		count = v
	}
	return start, count
}

// fail maps an Outline error to a SCIM error response. Upstream failures are
// logged here because the access log only carries the resulting status code.
func (s *Server) fail(w http.ResponseWriter, err error) {
	if errors.Is(err, outline.ErrNotFound) {
		writeError(w, http.StatusNotFound, "", "resource not found")
		return
	}
	slog.Error("outline request failed", "err", err)
	writeError(w, http.StatusBadGateway, "", err.Error())
}
