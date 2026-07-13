package scim

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/acul021/scim-outline-adapter/internal/outline"
)

// toSCIMGroup maps an Outline group plus its member ids to a SCIM group.
func toSCIMGroup(r *http.Request, g *outline.Group, memberIDs []string) Group {
	members := make([]Member, 0, len(memberIDs))
	for _, id := range memberIDs {
		members = append(members, Member{Value: id, Ref: location(r, "Users", id)})
	}
	return Group{
		Schemas:     []string{schemaGroup},
		ID:          g.ID,
		ExternalID:  g.ExternalID,
		DisplayName: g.Name,
		Members:     members,
		Meta:        &Meta{ResourceType: "Group", Location: location(r, "Groups", g.ID)},
	}
}

// findGroupByName returns the group whose name matches (case-insensitively), or
// nil. Used for idempotent adoption of an existing group on create.
func (s *Server) findGroupByName(ctx context.Context, name string) (*outline.Group, error) {
	groups, err := s.client.ListGroups(ctx)
	if err != nil {
		return nil, err
	}
	for i := range groups {
		if strings.EqualFold(groups[i].Name, name) {
			return &groups[i], nil
		}
	}
	return nil, nil
}

func (s *Server) createGroup(w http.ResponseWriter, r *http.Request) {
	var in Group
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeError(w, http.StatusBadRequest, "invalidSyntax", "malformed request body")
		return
	}
	if in.DisplayName == "" {
		writeError(w, http.StatusBadRequest, "invalidValue", "displayName is required")
		return
	}
	ctx := r.Context()

	existing, err := s.findGroupByName(ctx, in.DisplayName)
	if err != nil {
		s.fail(w, err)
		return
	}
	g := existing
	status := http.StatusCreated
	if existing != nil {
		status = http.StatusOK
		// Adopt: keep the externalId in sync if the client supplied one.
		if in.ExternalID != "" && in.ExternalID != existing.ExternalID {
			if g, err = s.client.UpdateGroup(ctx, existing.ID, "", in.ExternalID); err != nil {
				s.fail(w, err)
				return
			}
		}
	} else {
		if g, err = s.client.CreateGroup(ctx, in.DisplayName, in.ExternalID); err != nil {
			s.fail(w, err)
			return
		}
	}

	// Add any members supplied in the create payload, then recompute their roles.
	affected := make(map[string]bool)
	for _, m := range in.Members {
		if m.Value == "" {
			continue
		}
		if err := s.client.AddUserToGroup(ctx, g.ID, m.Value); err != nil {
			s.fail(w, err)
			return
		}
		affected[m.Value] = true
	}
	if err := s.recomputeRoles(ctx, affected); err != nil {
		s.fail(w, err)
		return
	}

	ids, err := s.client.GroupMemberships(ctx, g.ID)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, status, toSCIMGroup(r, g, ids))
}

func (s *Server) getGroup(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := r.PathValue("id")
	g, err := s.client.GetGroup(ctx, id)
	if err != nil {
		s.fail(w, err)
		return
	}
	ids, err := s.client.GroupMemberships(ctx, id)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toSCIMGroup(r, g, ids))
}

func (s *Server) listGroups(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	f, err := parseFilter(r.URL.Query().Get("filter"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalidFilter", err.Error())
		return
	}

	all, err := s.client.ListGroups(ctx)
	if err != nil {
		s.fail(w, err)
		return
	}

	var matched []outline.Group
	if f != nil {
		switch f.attr {
		case "displayname":
			for i := range all {
				if strings.EqualFold(all[i].Name, f.value) {
					matched = append(matched, all[i])
				}
			}
		case "externalid":
			for i := range all {
				if all[i].ExternalID == f.value {
					matched = append(matched, all[i])
				}
			}
		default:
			writeError(w, http.StatusBadRequest, "invalidFilter", "unsupported group filter attribute: "+f.attr)
			return
		}
	} else {
		matched = all
	}

	start, count := pageParams(r)
	resources := make([]any, 0)
	total := len(matched)
	for i := start - 1; i < total && len(resources) < count; i++ {
		ids, err := s.client.GroupMemberships(ctx, matched[i].ID)
		if err != nil {
			s.fail(w, err)
			return
		}
		resources = append(resources, toSCIMGroup(r, &matched[i], ids))
	}
	writeJSON(w, http.StatusOK, ListResponse{
		Schemas:      []string{schemaListResp},
		TotalResults: total,
		StartIndex:   start,
		ItemsPerPage: len(resources),
		Resources:    resources,
	})
}

func (s *Server) putGroup(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := r.PathValue("id")
	var in Group
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeError(w, http.StatusBadRequest, "invalidSyntax", "malformed request body")
		return
	}
	cur, err := s.client.GetGroup(ctx, id)
	if err != nil {
		s.fail(w, err)
		return
	}

	// Rename / externalId change.
	newName := ""
	if in.DisplayName != "" && in.DisplayName != cur.Name {
		newName = in.DisplayName
	}
	newExt := ""
	if in.ExternalID != "" && in.ExternalID != cur.ExternalID {
		newExt = in.ExternalID
	}
	if newName != "" || newExt != "" {
		if cur, err = s.client.UpdateGroup(ctx, id, newName, newExt); err != nil {
			s.fail(w, err)
			return
		}
	}

	// Reconcile the full member set against the desired list.
	desired := make([]string, 0, len(in.Members))
	for _, m := range in.Members {
		if m.Value != "" {
			desired = append(desired, m.Value)
		}
	}
	if err := s.reconcileMembers(ctx, id, desired); err != nil {
		s.fail(w, err)
		return
	}

	ids, err := s.client.GroupMemberships(ctx, id)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toSCIMGroup(r, cur, ids))
}

func (s *Server) patchGroup(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := r.PathValue("id")
	var req patchRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalidSyntax", "malformed request body")
		return
	}

	cur, err := s.client.GetGroup(ctx, id)
	if err != nil {
		s.fail(w, err)
		return
	}

	// Apply any displayName / externalId replaces before member ops.
	if name, ext, ok := groupAttrPatch(req.Operations); ok {
		if (name != "" && name != cur.Name) || (ext != "" && ext != cur.ExternalID) {
			if cur, err = s.client.UpdateGroup(ctx, id, name, ext); err != nil {
				s.fail(w, err)
				return
			}
		}
	}

	delta, err := applyGroupMemberPatch(req.Operations)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalidValue", err.Error())
		return
	}

	if delta.replaceAll {
		if err := s.reconcileMembers(ctx, id, delta.members); err != nil {
			s.fail(w, err)
			return
		}
	} else {
		affected := make(map[string]bool)
		for _, uid := range delta.add {
			if err := s.client.AddUserToGroup(ctx, id, uid); err != nil {
				s.fail(w, err)
				return
			}
			affected[uid] = true
		}
		for _, uid := range delta.remove {
			if err := s.client.RemoveUserFromGroup(ctx, id, uid); err != nil {
				s.fail(w, err)
				return
			}
			affected[uid] = true
		}
		if err := s.recomputeRoles(ctx, affected); err != nil {
			s.fail(w, err)
			return
		}
	}

	ids, err := s.client.GroupMemberships(ctx, id)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toSCIMGroup(r, cur, ids))
}

func (s *Server) deleteGroup(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := r.PathValue("id")

	// Capture members before deletion so their roles can be recomputed after the
	// group (and its membership edges) are gone.
	former, err := s.client.GroupMemberships(ctx, id)
	if err != nil {
		s.fail(w, err)
		return
	}
	if err := s.client.DeleteGroup(ctx, id); err != nil {
		s.fail(w, err)
		return
	}
	affected := make(map[string]bool, len(former))
	for _, uid := range former {
		affected[uid] = true
	}
	if err := s.recomputeRoles(ctx, affected); err != nil {
		s.fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// reconcileMembers makes the group's membership exactly desired, adding and
// removing as needed, and recomputes roles for every user whose membership
// changed.
func (s *Server) reconcileMembers(ctx context.Context, groupID string, desired []string) error {
	current, err := s.client.GroupMemberships(ctx, groupID)
	if err != nil {
		return err
	}
	curSet := toSet(current)
	wantSet := toSet(desired)
	affected := make(map[string]bool)

	for uid := range wantSet {
		if !curSet[uid] {
			if err := s.client.AddUserToGroup(ctx, groupID, uid); err != nil {
				return err
			}
			affected[uid] = true
		}
	}
	for uid := range curSet {
		if !wantSet[uid] {
			if err := s.client.RemoveUserFromGroup(ctx, groupID, uid); err != nil {
				return err
			}
			affected[uid] = true
		}
	}
	return s.recomputeRoles(ctx, affected)
}

// recomputeRoles recomputes the Outline role for each affected user.
func (s *Server) recomputeRoles(ctx context.Context, users map[string]bool) error {
	for uid := range users {
		if err := s.recomputeRole(ctx, uid); err != nil {
			return err
		}
	}
	return nil
}

// groupAttrPatch extracts a displayName and/or externalId replace from PATCH
// operations. ok is true if either was present.
func groupAttrPatch(ops []patchOp) (name, ext string, ok bool) {
	for _, op := range ops {
		if strings.ToLower(op.Op) != "replace" && strings.ToLower(op.Op) != "add" {
			continue
		}
		lpath := strings.ToLower(strings.TrimSpace(op.Path))
		switch lpath {
		case "displayname":
			if s, err := decodeString(op.Value); err == nil {
				name, ok = s, true
			}
		case "externalid":
			if s, err := decodeString(op.Value); err == nil {
				ext, ok = s, true
			}
		case "":
			var attrs map[string]json.RawMessage
			if json.Unmarshal(op.Value, &attrs) == nil {
				if v, has := attrs["displayName"]; has {
					if s, err := decodeString(v); err == nil {
						name, ok = s, true
					}
				}
				if v, has := attrs["externalId"]; has {
					if s, err := decodeString(v); err == nil {
						ext, ok = s, true
					}
				}
			}
		}
	}
	return name, ext, ok
}

// toSet builds a set from a slice.
func toSet(ss []string) map[string]bool {
	m := make(map[string]bool, len(ss))
	for _, s := range ss {
		m[s] = true
	}
	return m
}
