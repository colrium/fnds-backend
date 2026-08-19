package workspaces

import (
	"testing"

	"pgregory.net/rapid"
)

// Feature: stytch-employer-onboarding, Property 1: The Permission_Matrix is a total function with an Owner superset
// Validates: Requirements 7.1, 7.2, 7.3, 7.4, 7.5, 7.6, 7.18
func TestProperty1_PermissionMatrixTotalWithOwnerSuperset(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		role := rapid.SampledFrom(AllRoles[:]).Draw(t, "role")
		perm := rapid.SampledFrom(AllPermissions[:]).Draw(t, "perm")

		// Total function: never panics, always returns a boolean.
		_ = Grants(role, perm)

		// Owner is a superset of the union of admin, hr, and finance.
		for _, p := range GrantedPermissions(RoleAdmin) {
			if !Grants(RoleOwner, p) {
				t.Fatalf("owner missing admin permission %s", p)
			}
		}
		for _, p := range GrantedPermissions(RoleHR) {
			if !Grants(RoleOwner, p) {
				t.Fatalf("owner missing hr permission %s", p)
			}
		}
		for _, p := range GrantedPermissions(RoleFinance) {
			if !Grants(RoleOwner, p) {
				t.Fatalf("owner missing finance permission %s", p)
			}
		}
	})
}

// Feature: stytch-employer-onboarding, Property 1b: an unknown role/permission never panics
// Validates: Requirements 7.13, 7.14
func TestProperty1_UnknownRoleOrPermissionNeverPanics(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		role := Role(rapid.String().Draw(t, "role"))
		perm := Permission(rapid.String().Draw(t, "perm"))
		_ = Grants(role, perm)
	})
}

// Feature: stytch-employer-onboarding, Property 2: Restricted Permissions are never reachable from a non-Owner Workspace_Role
// Validates: Requirements 7.7, 7.8, 7.9, 24.6, 24.7
func TestProperty2_RestrictedPermissionsNeverReachableFromNonOwner(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		role := rapid.SampledFrom([]Role{RoleAdmin, RoleHR, RoleFinance}).Draw(t, "role")

		if Grants(role, PermWorkspaceDelete) {
			t.Fatalf("%s must not grant workspace.delete", role)
		}
		if Grants(role, PermOwnershipTransfer) {
			t.Fatalf("%s must not grant ownership.transfer", role)
		}

		if role == RoleFinance {
			return
		}
		for _, p := range []Permission{PermBillingManage, PermPaymentMethodManage, PermInvoicePay, PermSubscriptionManage} {
			if Grants(role, p) {
				t.Fatalf("%s must not grant %s", role, p)
			}
		}
	})
}
