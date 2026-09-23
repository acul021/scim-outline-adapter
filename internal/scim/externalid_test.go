package scim

import (
	"net/http"
	"path/filepath"
	"testing"

	"github.com/acul021/scim-outline-adapter/internal/extid"
	"github.com/acul021/scim-outline-adapter/internal/outline"
)

func testServerWithExternalIDs(t *testing.T, hardDelete bool) (*Server, *fakeClient, *extid.FileStore) {
	t.Helper()
	store, err := extid.Open(filepath.Join(t.TempDir(), "extid.json"))
	if err != nil {
		t.Fatal(err)
	}
	fc := newFake()
	s := NewServer(fc, RoleMap{}, testToken, hardDelete).WithExternalIDStore(store)
	return s, fc, store
}

func listByExternalID(t *testing.T, h http.Handler, ext string) ListResponse {
	t.Helper()
	var lr ListResponse
	decode(t, do(t, h, "GET", `/scim/v2/Users?filter=externalId+eq+%22`+ext+`%22`, ""), &lr)
	return lr
}

func TestExternalIDDroppedWithoutStore(t *testing.T) {
	s, _ := testServer()
	rec := do(t, s.Handler(), "POST", "/scim/v2/Users",
		`{"userName":"a","externalId":"pid-a","emails":[{"value":"a@example.com","primary":true}]}`)
	var u User
	decode(t, rec, &u)
	if u.ExternalID != "" {
		t.Fatalf("externalId returned without a store: %q", u.ExternalID)
	}
}

func TestExternalIDCreateAndFilter(t *testing.T) {
	s, _, _ := testServerWithExternalIDs(t, false)
	h := s.Handler()

	rec := do(t, h, "POST", "/scim/v2/Users",
		`{"userName":"a","externalId":"pid-a","emails":[{"value":"a@example.com","primary":true}]}`)
	var u User
	decode(t, rec, &u)
	if u.ExternalID != "pid-a" {
		t.Fatalf("create response externalId = %q", u.ExternalID)
	}

	lr := listByExternalID(t, h, "pid-a")
	if lr.TotalResults != 1 {
		t.Fatalf("filter by externalId: want 1, got %d", lr.TotalResults)
	}

	var all ListResponse
	decode(t, do(t, h, "GET", "/scim/v2/Users", ""), &all)
	if all.Resources[0].(map[string]any)["externalId"] != "pid-a" {
		t.Fatalf("list lacks externalId: %+v", all.Resources[0])
	}
}

// A user that existed before the mapping was enabled has no entry. It keeps
// working by Outline id and gains an externalId once a client adopts it.
func TestExternalIDAdoptsPreexistingUser(t *testing.T) {
	s, fc, _ := testServerWithExternalIDs(t, false)
	h := s.Handler()
	fc.users["old"] = &outline.User{ID: "old", Email: "old@example.com", Name: "Old", Role: roleMember}

	var u User
	decode(t, do(t, h, "GET", "/scim/v2/Users/old", ""), &u)
	if u.ExternalID != "" {
		t.Fatalf("unmapped user has externalId %q", u.ExternalID)
	}
	if lr := listByExternalID(t, h, "pid-old"); lr.TotalResults != 0 {
		t.Fatalf("unmapped externalId matched %d users", lr.TotalResults)
	}

	rec := do(t, h, "POST", "/scim/v2/Users",
		`{"userName":"old","externalId":"pid-old","emails":[{"value":"old@example.com","primary":true}]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("adopt: want 200, got %d", rec.Code)
	}
	decode(t, rec, &u)
	if u.ID != "old" || u.ExternalID != "pid-old" {
		t.Fatalf("adopted user = %+v", u)
	}
}

func TestExternalIDPutAndPatch(t *testing.T) {
	s, _, store := testServerWithExternalIDs(t, false)
	h := s.Handler()
	var u User
	decode(t, do(t, h, "POST", "/scim/v2/Users", `{"userName":"a","emails":[{"value":"a@example.com","primary":true}]}`), &u)

	do(t, h, "PUT", "/scim/v2/Users/"+u.ID, `{"userName":"a","externalId":"ext-put","active":true}`)
	if got := store.Get(u.ID); got != "ext-put" {
		t.Fatalf("after PUT: %q", got)
	}

	// A PUT without externalId keeps the stored one.
	do(t, h, "PUT", "/scim/v2/Users/"+u.ID, `{"userName":"a","active":true}`)
	if got := store.Get(u.ID); got != "ext-put" {
		t.Fatalf("PUT without externalId cleared it: %q", got)
	}

	do(t, h, "PATCH", "/scim/v2/Users/"+u.ID,
		`{"Operations":[{"op":"replace","path":"externalId","value":"ext-patch"}]}`)
	if got := store.Get(u.ID); got != "ext-patch" {
		t.Fatalf("after PATCH: %q", got)
	}
}

func TestExternalIDDelete(t *testing.T) {
	create := `{"userName":"a","externalId":"pid-a","emails":[{"value":"a@example.com","primary":true}]}`

	s, _, store := testServerWithExternalIDs(t, false)
	h := s.Handler()
	var u User
	decode(t, do(t, h, "POST", "/scim/v2/Users", create), &u)
	do(t, h, "DELETE", "/scim/v2/Users/"+u.ID, "")
	if store.Get(u.ID) != "pid-a" {
		t.Fatal("soft delete dropped the mapping")
	}

	s, _, store = testServerWithExternalIDs(t, true)
	h = s.Handler()
	decode(t, do(t, h, "POST", "/scim/v2/Users", create), &u)
	do(t, h, "DELETE", "/scim/v2/Users/"+u.ID, "")
	if _, ok := store.Lookup("pid-a"); ok {
		t.Fatal("hard delete kept the mapping")
	}
}

func TestExternalIDStaleEntryDropped(t *testing.T) {
	s, fc, store := testServerWithExternalIDs(t, false)
	h := s.Handler()
	var u User
	decode(t, do(t, h, "POST", "/scim/v2/Users",
		`{"userName":"a","externalId":"pid-a","emails":[{"value":"a@example.com","primary":true}]}`), &u)

	delete(fc.users, u.ID) // removed in Outline directly
	if lr := listByExternalID(t, h, "pid-a"); lr.TotalResults != 0 {
		t.Fatalf("stale mapping matched %d users", lr.TotalResults)
	}
	if _, ok := store.Lookup("pid-a"); ok {
		t.Fatal("stale mapping not dropped")
	}
}
