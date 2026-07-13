package scim

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const testToken = "devtoken"

func testServer() (*Server, *fakeClient) {
	fc := newFake()
	rm := RoleMap{
		Admin:  []string{"Admins"},
		Member: []string{"Staff", "Editors"},
		Viewer: []string{"Viewers"},
	}
	return NewServer(fc, rm, testToken, false), fc
}

// do issues an authenticated SCIM request and returns the recorder.
func do(t *testing.T, h http.Handler, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	var r io.Reader
	if body != "" {
		r = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, r)
	req.Header.Set("Authorization", "Bearer "+testToken)
	req.Header.Set("Content-Type", contentType)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func decode(t *testing.T, rec *httptest.ResponseRecorder, v any) {
	t.Helper()
	if err := json.Unmarshal(rec.Body.Bytes(), v); err != nil {
		t.Fatalf("decode body %q: %v", rec.Body.String(), err)
	}
}

func TestAuthRequired(t *testing.T) {
	s, _ := testServer()
	h := s.Handler()
	req := httptest.NewRequest("GET", "/scim/v2/Users", nil) // no token
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d", rec.Code)
	}
}

func TestServiceProviderConfig(t *testing.T) {
	s, _ := testServer()
	rec := do(t, s.Handler(), "GET", "/scim/v2/ServiceProviderConfig", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	var cfg map[string]any
	decode(t, rec, &cfg)
	if patch := cfg["patch"].(map[string]any); patch["supported"] != true {
		t.Fatal("expected patch.supported=true")
	}
}

func TestUserCreateGetAndAdopt(t *testing.T) {
	s, fc := testServer()
	h := s.Handler()

	rec := do(t, h, "POST", "/scim/v2/Users",
		`{"schemas":["urn:ietf:params:scim:schemas:core:2.0:User"],"userName":"scim-test-1@example.com","name":{"givenName":"Scim","familyName":"Test"},"externalId":"ext-1","active":true}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: want 201, got %d body=%s", rec.Code, rec.Body.String())
	}
	var u User
	decode(t, rec, &u)
	if u.ID == "" || u.UserName != "scim-test-1@example.com" || !u.Active {
		t.Fatalf("unexpected created user: %+v", u)
	}
	if fc.users[u.ID].Role != roleViewer {
		t.Fatalf("new user should default to viewer, got %q", fc.users[u.ID].Role)
	}

	// GET the user back.
	rec = do(t, h, "GET", "/scim/v2/Users/"+u.ID, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("get: want 200, got %d", rec.Code)
	}

	// Re-create with the same email -> adoption returns 200, same id.
	rec = do(t, h, "POST", "/scim/v2/Users",
		`{"userName":"scim-test-1@example.com","name":{"givenName":"Scim","familyName":"Test"}}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("adopt: want 200, got %d", rec.Code)
	}
	var u2 User
	decode(t, rec, &u2)
	if u2.ID != u.ID {
		t.Fatalf("adoption returned different id: %s vs %s", u2.ID, u.ID)
	}
}

func TestUserListFilterByUserName(t *testing.T) {
	s, _ := testServer()
	h := s.Handler()
	do(t, h, "POST", "/scim/v2/Users", `{"userName":"a@example.com"}`)

	rec := do(t, h, "GET", `/scim/v2/Users?filter=userName+eq+%22a@example.com%22`, "")
	var lr ListResponse
	decode(t, rec, &lr)
	if lr.TotalResults != 1 {
		t.Fatalf("want 1 result, got %d", lr.TotalResults)
	}

	// A non-matching filter returns an empty list, not an error.
	rec = do(t, h, "GET", `/scim/v2/Users?filter=userName+eq+%22missing@example.com%22`, "")
	decode(t, rec, &lr)
	if lr.TotalResults != 0 {
		t.Fatalf("want 0 results, got %d", lr.TotalResults)
	}
}

func TestUserPatchActive(t *testing.T) {
	s, fc := testServer()
	h := s.Handler()
	rec := do(t, h, "POST", "/scim/v2/Users", `{"userName":"c@example.com"}`)
	var u User
	decode(t, rec, &u)

	rec = do(t, h, "PATCH", "/scim/v2/Users/"+u.ID,
		`{"Operations":[{"op":"replace","path":"active","value":false}]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("patch: want 200, got %d", rec.Code)
	}
	if !fc.users[u.ID].IsSuspended {
		t.Fatal("user should be suspended")
	}

	// Reactivate.
	do(t, h, "PATCH", "/scim/v2/Users/"+u.ID, `{"Operations":[{"op":"replace","path":"active","value":true}]}`)
	if fc.users[u.ID].IsSuspended {
		t.Fatal("user should be active again")
	}
}

func TestUserDeleteSuspends(t *testing.T) {
	s, fc := testServer() // hardDelete=false
	h := s.Handler()
	rec := do(t, h, "POST", "/scim/v2/Users", `{"userName":"d@example.com"}`)
	var u User
	decode(t, rec, &u)

	rec = do(t, h, "DELETE", "/scim/v2/Users/"+u.ID, "")
	if rec.Code != http.StatusNoContent {
		t.Fatalf("delete: want 204, got %d", rec.Code)
	}
	if _, ok := fc.users[u.ID]; !ok {
		t.Fatal("soft delete must not remove the user")
	}
	if !fc.users[u.ID].IsSuspended {
		t.Fatal("soft delete must suspend the user")
	}
}

// TestRoleDerivationFlow mirrors the authentik provisioning sequence: create a
// user and two groups, then move the user between them and confirm the Outline
// role tracks membership.
func TestRoleDerivationFlow(t *testing.T) {
	s, fc := testServer()
	h := s.Handler()

	rec := do(t, h, "POST", "/scim/v2/Users", `{"userName":"member@example.com"}`)
	var u User
	decode(t, rec, &u)

	rec = do(t, h, "POST", "/scim/v2/Groups", `{"displayName":"Editors","externalId":"g-ref"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("group create: %d", rec.Code)
	}
	var gRef Group
	decode(t, rec, &gRef)
	if gRef.ExternalID != "g-ref" {
		t.Fatalf("group externalId not round-tripped: %+v", gRef)
	}

	rec = do(t, h, "POST", "/scim/v2/Groups", `{"displayName":"Admins"}`)
	var gAdmin Group
	decode(t, rec, &gAdmin)

	// Add to Editors -> member.
	do(t, h, "PATCH", "/scim/v2/Groups/"+gRef.ID,
		`{"Operations":[{"op":"add","path":"members","value":[{"value":"`+u.ID+`"}]}]}`)
	if fc.users[u.ID].Role != roleMember {
		t.Fatalf("after Editors add, want member, got %q", fc.users[u.ID].Role)
	}

	// Add to Admins -> admin.
	do(t, h, "PATCH", "/scim/v2/Groups/"+gAdmin.ID,
		`{"Operations":[{"op":"add","path":"members","value":[{"value":"`+u.ID+`"}]}]}`)
	if fc.users[u.ID].Role != roleAdmin {
		t.Fatalf("after Admins add, want admin, got %q", fc.users[u.ID].Role)
	}

	// Remove from Admins -> back to member.
	do(t, h, "PATCH", "/scim/v2/Groups/"+gAdmin.ID,
		`{"Operations":[{"op":"remove","path":"members[value eq \"`+u.ID+`\"]"}]}`)
	if fc.users[u.ID].Role != roleMember {
		t.Fatalf("after Admins remove, want member, got %q", fc.users[u.ID].Role)
	}

	// Delete Editors -> role recomputes to viewer (no groups left).
	rec = do(t, h, "DELETE", "/scim/v2/Groups/"+gRef.ID, "")
	if rec.Code != http.StatusNoContent {
		t.Fatalf("group delete: %d", rec.Code)
	}
	if fc.users[u.ID].Role != roleViewer {
		t.Fatalf("after group delete, want viewer, got %q", fc.users[u.ID].Role)
	}
}

// TestSuspendedUserRoleRecomputeSkipped reproduces the live edge case where a
// group holding a suspended member is deleted: Outline rejects update_role on a
// suspended user, so the recompute must be skipped rather than fail the request.
func TestSuspendedUserRoleRecomputeSkipped(t *testing.T) {
	s, fc := testServer()
	h := s.Handler()

	var u User
	decode(t, do(t, h, "POST", "/scim/v2/Users", `{"userName":"susp@example.com"}`), &u)
	var g Group
	decode(t, do(t, h, "POST", "/scim/v2/Groups", `{"displayName":"Editors"}`), &g)
	do(t, h, "PATCH", "/scim/v2/Groups/"+g.ID,
		`{"Operations":[{"op":"add","path":"members","value":[{"value":"`+u.ID+`"}]}]}`)

	// Suspend, then simulate Outline's refusal to change a suspended user's role.
	fc.users[u.ID].IsSuspended = true
	fc.roleUpdateForbiddenIfSuspended = true

	rec := do(t, h, "DELETE", "/scim/v2/Groups/"+g.ID, "")
	if rec.Code != http.StatusNoContent {
		t.Fatalf("delete group with suspended member: want 204, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestGroupPutReconcilesMembers(t *testing.T) {
	s, fc := testServer()
	h := s.Handler()

	// Two users.
	var u1, u2 User
	decode(t, do(t, h, "POST", "/scim/v2/Users", `{"userName":"one@example.com"}`), &u1)
	decode(t, do(t, h, "POST", "/scim/v2/Users", `{"userName":"two@example.com"}`), &u2)

	var g Group
	decode(t, do(t, h, "POST", "/scim/v2/Groups", `{"displayName":"Staff","members":[{"value":"`+u1.ID+`"}]}`), &g)
	if !fc.members[g.ID][u1.ID] {
		t.Fatal("u1 should be a member after create")
	}

	// PUT replaces membership: now only u2.
	rec := do(t, h, "PUT", "/scim/v2/Groups/"+g.ID,
		`{"displayName":"Staff","members":[{"value":"`+u2.ID+`"}]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("put: %d", rec.Code)
	}
	if fc.members[g.ID][u1.ID] {
		t.Fatal("u1 should have been removed")
	}
	if !fc.members[g.ID][u2.ID] {
		t.Fatal("u2 should have been added")
	}
	if fc.users[u1.ID].Role != roleViewer {
		t.Fatalf("u1 should be viewer after removal, got %q", fc.users[u1.ID].Role)
	}
	if fc.users[u2.ID].Role != roleMember {
		t.Fatalf("u2 should be member, got %q", fc.users[u2.ID].Role)
	}
}

func TestGroupFilterByDisplayName(t *testing.T) {
	s, _ := testServer()
	h := s.Handler()
	do(t, h, "POST", "/scim/v2/Groups", `{"displayName":"Viewers"}`)

	rec := do(t, h, "GET", `/scim/v2/Groups?filter=displayName+eq+%22Viewers%22`, "")
	var lr ListResponse
	decode(t, rec, &lr)
	if lr.TotalResults != 1 {
		t.Fatalf("want 1, got %d", lr.TotalResults)
	}
}

func TestNotFound(t *testing.T) {
	s, _ := testServer()
	rec := do(t, s.Handler(), "GET", "/scim/v2/Users/does-not-exist", "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("want 404, got %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != contentType {
		t.Fatalf("want %s content type, got %s", contentType, ct)
	}
}
