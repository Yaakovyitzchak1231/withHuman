package domain

import (
	"slices"
	"testing"
)

func TestBuiltInRolesFollowTheRegistry(t *testing.T) {
	owner, _ := BuiltInRole(RoleOwner)
	for _, def := range Permissions() {
		if def.HeldBy(HolderHuman) && !slices.Contains(owner.Permissions, def.Key) {
			t.Fatalf("owner must hold every human permission, missing %s", def.Key)
		}
		if !def.HeldBy(HolderHuman) && slices.Contains(owner.Permissions, def.Key) {
			t.Fatalf("owner must not hold the machine permission %s", def.Key)
		}
	}
	for _, def := range BuiltInRoles() {
		for _, key := range def.Permissions {
			if _, ok := LookupPermission(key); !ok {
				t.Fatalf("role %s names unregistered permission %s", def.Key, key)
			}
		}
		if len(def.BindableScopeKinds) == 0 || def.BindableScopeKinds[0] != ScopeOrganization {
			t.Fatalf("role %s must be bindable at organization scope first", def.Key)
		}
	}
}

func TestPipelineWriteBelongsToOwnersAndAdminsOnly(t *testing.T) {
	for _, role := range []Role{RoleOwner, RoleAdmin} {
		def, _ := BuiltInRole(role)
		if !slices.Contains(def.Permissions, PermissionPipelineWrite) {
			t.Fatalf("%s should hold pipeline.write", role)
		}
	}
	for _, role := range []Role{RoleReviewer, RoleAuditor, RoleAgent} {
		def, _ := BuiltInRole(role)
		if slices.Contains(def.Permissions, PermissionPipelineWrite) {
			t.Fatalf("%s should not hold pipeline.write", role)
		}
	}
}

func TestRegisterPermissionsRefusesBadKeys(t *testing.T) {
	cases := []PermissionDefinition{
		{Key: "billing.read", Holders: []PermissionHolder{HolderHuman}, Resource: ResourceOrganization},
		{Key: "Billing", Holders: []PermissionHolder{HolderHuman}, Resource: ResourceOrganization},
		{Key: "billing.write", Resource: ResourceOrganization},
		{Key: "billing.export", Holders: []PermissionHolder{"robot"}, Resource: ResourceOrganization},
		{Key: "billing.audit", Holders: []PermissionHolder{HolderHuman}, Resource: "ledger"},
	}
	if err := RegisterPermissions(cases[0]); err != nil {
		t.Fatalf("valid edition key refused: %v", err)
	}
	if err := RegisterPermissions(cases[0]); err == nil {
		t.Fatal("duplicate key must be refused")
	}
	for _, bad := range cases[1:] {
		if err := RegisterPermissions(bad); err == nil {
			t.Fatalf("expected refusal for %#v", bad)
		}
	}
	if !slices.Contains(HumanPermissions(), "billing.read") {
		t.Fatal("registered edition key must reach the human permission list")
	}
}

func TestResourceCoveringScopes(t *testing.T) {
	scopes := AgentResource("refund-bot").CoveringScopes()
	if len(scopes) != 2 || scopes[1] != (Scope{Kind: ScopeAgent, ID: "refund-bot"}) {
		t.Fatalf("unexpected covering scopes %#v", scopes)
	}
	if got := RequestResource("r").CoveringScopes(); len(got) != 1 || got[0].Kind != ScopeOrganization {
		t.Fatalf("requests are covered by organization scope only, got %#v", got)
	}
	pipeline := PipelineResource("payments").CoveringScopes()
	if len(pipeline) != 2 || pipeline[1] != (Scope{Kind: ScopePipeline, ID: "payments"}) {
		t.Fatalf("unexpected pipeline covering scopes %#v", pipeline)
	}
	if got := (Scope{Kind: ScopePipeline, ID: "payments"}).Resource(); got != PipelineResource("payments") {
		t.Fatalf("pipeline scope must map back to the pipeline resource, got %#v", got)
	}
}
