package scim

import (
	"encoding/json"
	"testing"
)

func mustOps(t *testing.T, body string) []patchOp {
	t.Helper()
	var req patchRequest
	if err := json.Unmarshal([]byte(body), &req); err != nil {
		t.Fatalf("unmarshal ops: %v", err)
	}
	return req.Operations
}

func TestApplyUserPatch_Active(t *testing.T) {
	ops := mustOps(t, `{"Operations":[{"op":"replace","path":"active","value":false}]}`)
	res, err := applyUserPatch(ops)
	if err != nil {
		t.Fatal(err)
	}
	if res.setActive == nil || *res.setActive != false {
		t.Fatalf("expected setActive=false, got %v", res.setActive)
	}
}

func TestApplyUserPatch_ActiveStringBool(t *testing.T) {
	// authentik sometimes sends the boolean as a quoted string.
	ops := mustOps(t, `{"Operations":[{"op":"replace","path":"active","value":"False"}]}`)
	res, err := applyUserPatch(ops)
	if err != nil {
		t.Fatal(err)
	}
	if res.setActive == nil || *res.setActive != false {
		t.Fatalf("expected setActive=false, got %v", res.setActive)
	}
}

func TestApplyUserPatch_PathlessName(t *testing.T) {
	ops := mustOps(t, `{"Operations":[{"op":"replace","value":{"name":{"givenName":"Ada","familyName":"Lovelace"},"active":true}}]}`)
	res, err := applyUserPatch(ops)
	if err != nil {
		t.Fatal(err)
	}
	if res.setName == nil || *res.setName != "Ada Lovelace" {
		t.Fatalf("expected name 'Ada Lovelace', got %v", res.setName)
	}
	if res.setActive == nil || *res.setActive != true {
		t.Fatalf("expected setActive=true, got %v", res.setActive)
	}
}

func TestApplyUserPatch_UnsupportedOp(t *testing.T) {
	ops := mustOps(t, `{"Operations":[{"op":"invent","path":"active","value":true}]}`)
	if _, err := applyUserPatch(ops); err == nil {
		t.Fatal("expected error for unsupported op")
	}
}

func TestApplyGroupMemberPatch_AddRemove(t *testing.T) {
	ops := mustOps(t, `{"Operations":[
		{"op":"add","path":"members","value":[{"value":"u1"},{"value":"u2"}]},
		{"op":"remove","path":"members[value eq \"u3\"]"}
	]}`)
	d, err := applyGroupMemberPatch(ops)
	if err != nil {
		t.Fatal(err)
	}
	if len(d.add) != 2 || d.add[0] != "u1" || d.add[1] != "u2" {
		t.Fatalf("unexpected add: %v", d.add)
	}
	if len(d.remove) != 1 || d.remove[0] != "u3" {
		t.Fatalf("unexpected remove: %v", d.remove)
	}
	if d.replaceAll {
		t.Fatal("did not expect replaceAll")
	}
}

func TestApplyGroupMemberPatch_Replace(t *testing.T) {
	ops := mustOps(t, `{"Operations":[{"op":"replace","path":"members","value":[{"value":"u9"}]}]}`)
	d, err := applyGroupMemberPatch(ops)
	if err != nil {
		t.Fatal(err)
	}
	if !d.replaceAll || len(d.members) != 1 || d.members[0] != "u9" {
		t.Fatalf("unexpected replace delta: %+v", d)
	}
}

func TestApplyGroupMemberPatch_RemoveByValueList(t *testing.T) {
	ops := mustOps(t, `{"Operations":[{"op":"remove","path":"members","value":[{"value":"u5"}]}]}`)
	d, err := applyGroupMemberPatch(ops)
	if err != nil {
		t.Fatal(err)
	}
	if len(d.remove) != 1 || d.remove[0] != "u5" {
		t.Fatalf("unexpected remove: %+v", d)
	}
}

func TestGroupAttrPatch(t *testing.T) {
	ops := mustOps(t, `{"Operations":[{"op":"replace","path":"displayName","value":"NewName"}]}`)
	name, _, ok := groupAttrPatch(ops)
	if !ok || name != "NewName" {
		t.Fatalf("expected NewName, got %q ok=%v", name, ok)
	}
}
