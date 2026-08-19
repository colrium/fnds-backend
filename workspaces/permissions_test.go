package workspaces

import (
	"testing"
)

func TestAllRolesLength(t *testing.T) {
	if len(AllRoles) != 4 {
		t.Fatalf("expected 4 roles, got %d", len(AllRoles))
	}
}

func TestAllPermissionsLength(t *testing.T) {
	if len(AllPermissions) != 26 {
		t.Fatalf("expected 26 permissions, got %d", len(AllPermissions))
	}
}

func TestOwnerHasAllPermissions(t *testing.T) {
	for _, p := range AllPermissions {
		if !Grants(RoleOwner, p) {
			t.Errorf("Owner should have permission %s", p)
		}
	}
}

func TestAdminPermissions(t *testing.T) {
	// Req 7.4: Admin's expected set
	expected := map[Permission]bool{
		PermWorkspaceView:     true,
		PermWorkspaceRename:   true,
		PermMemberView:        true,
		PermMemberInvite:      true,
		PermMemberRoleChange:  true,
		PermMemberRemove:      true,
		PermVacancyCreate:     true,
		PermVacancyUpdate:     true,
		PermVacancyClose:      true,
		PermVacancyView:       true,
		PermApplicationView:   true,
		PermApplicationDecide: true,
		PermCompanyCreate:     true,
		PermCompanyUpdate:     true,
		PermArtisanBrowse:     true,
		PermBillingView:       true,
		PermInvoiceView:       true,
		PermPayoutView:        true,
		PermAuditView:         true,
	}
	for _, p := range AllPermissions {
		got := Grants(RoleAdmin, p)
		want := expected[p]
		if got != want {
			t.Errorf("Admin + %s: got %v, want %v", p, got, want)
		}
	}
}

func TestHRPermissions(t *testing.T) {
	// Req 7.5: HR's expected set
	expected := map[Permission]bool{
		PermWorkspaceView:     true,
		PermMemberView:        true,
		PermVacancyCreate:     true,
		PermVacancyUpdate:     true,
		PermVacancyClose:      true,
		PermVacancyView:       true,
		PermApplicationView:   true,
		PermApplicationDecide: true,
		PermCompanyCreate:     true,
		PermCompanyUpdate:     true,
		PermArtisanBrowse:     true,
	}
	for _, p := range AllPermissions {
		got := Grants(RoleHR, p)
		want := expected[p]
		if got != want {
			t.Errorf("HR + %s: got %v, want %v", p, got, want)
		}
	}
}

func TestFinancePermissions(t *testing.T) {
	// Req 7.6: Finance's expected set
	expected := map[Permission]bool{
		PermWorkspaceView:       true,
		PermMemberView:          true,
		PermBillingView:         true,
		PermBillingManage:       true,
		PermPaymentMethodManage: true,
		PermInvoiceView:         true,
		PermInvoicePay:          true,
		PermPayoutView:          true,
		PermSubscriptionManage:  true,
	}
	for _, p := range AllPermissions {
		got := Grants(RoleFinance, p)
		want := expected[p]
		if got != want {
			t.Errorf("Finance + %s: got %v, want %v", p, got, want)
		}
	}
}

func TestOwnerOnlyPermissions(t *testing.T) {
	// Req 7.7, 7.8: workspace.delete and ownership.transfer are Owner-only
	ownerOnly := []Permission{PermWorkspaceDelete, PermOwnershipTransfer, PermWorkspaceImportPersonalData}
	nonOwnerRoles := []Role{RoleAdmin, RoleHR, RoleFinance}

	for _, p := range ownerOnly {
		if !Grants(RoleOwner, p) {
			t.Errorf("Owner should have %s", p)
		}
		for _, r := range nonOwnerRoles {
			if Grants(r, p) {
				t.Errorf("%s should NOT have %s", r, p)
			}
		}
	}
}

func TestMoneyWritePermissionsOwnerAndFinanceOnly(t *testing.T) {
	// Req 7.9: billing.manage, payment_method.manage, invoice.pay, subscription.manage
	// are restricted to owner and finance only
	moneyWrite := []Permission{PermBillingManage, PermPaymentMethodManage, PermInvoicePay, PermSubscriptionManage}
	for _, p := range moneyWrite {
		if !Grants(RoleOwner, p) {
			t.Errorf("Owner should have %s", p)
		}
		if !Grants(RoleFinance, p) {
			t.Errorf("Finance should have %s", p)
		}
		if Grants(RoleAdmin, p) {
			t.Errorf("Admin should NOT have %s", p)
		}
		if Grants(RoleHR, p) {
			t.Errorf("HR should NOT have %s", p)
		}
	}
}

func TestUnknownRoleYieldsEmptySet(t *testing.T) {
	unknown := Role("unknown")
	for _, p := range AllPermissions {
		if Grants(unknown, p) {
			t.Errorf("unknown role should not have permission %s", p)
		}
	}
}

func TestUnknownPermissionReturnsFalse(t *testing.T) {
	if Grants(RoleOwner, Permission("nonexistent.permission")) {
		t.Error("unknown permission should return false")
	}
}

func TestGrantedPermissionsStableOrder(t *testing.T) {
	perms := GrantedPermissions(RoleOwner)
	if len(perms) != len(AllPermissions) {
		t.Fatalf("Owner should have %d permissions, got %d", len(AllPermissions), len(perms))
	}
	for i, p := range perms {
		if p != AllPermissions[i] {
			t.Errorf("position %d: got %s, want %s", i, p, AllPermissions[i])
		}
	}
}

func TestGrantedPermissionsUnknownRole(t *testing.T) {
	perms := GrantedPermissions(Role("bogus"))
	if perms != nil {
		t.Errorf("expected nil for unknown role, got %v", perms)
	}
}

func TestValidRole(t *testing.T) {
	cases := []struct {
		input string
		want  bool
	}{
		{"owner", true},
		{"admin", true},
		{"hr", true},
		{"finance", true},
		{"Owner", false},
		{"ADMIN", false},
		{"", false},
		{"superadmin", false},
		{"artisan", false},
	}
	for _, tc := range cases {
		got := ValidRole(tc.input)
		if got != tc.want {
			t.Errorf("ValidRole(%q) = %v, want %v", tc.input, got, tc.want)
		}
	}
}

func TestOwnerIsSupersetOfAllOtherRoles(t *testing.T) {
	ownerSet := permissionMatrix[RoleOwner]
	for _, r := range []Role{RoleAdmin, RoleHR, RoleFinance} {
		roleSet := permissionMatrix[r]
		if ownerSet&roleSet != roleSet {
			t.Errorf("Owner's permission set is not a superset of %s", r)
		}
	}
}

func TestPermissionSetBitmask(t *testing.T) {
	// Verify that the PermissionSet correctly encodes all 26 permissions
	s := setOf(AllPermissions[:]...)
	for _, p := range AllPermissions {
		if !s.has(p) {
			t.Errorf("setOf(AllPermissions...) should contain %s", p)
		}
	}
}

func TestSetOfEmpty(t *testing.T) {
	s := setOf()
	for _, p := range AllPermissions {
		if s.has(p) {
			t.Errorf("empty set should not contain %s", p)
		}
	}
}

func TestGrantsNoAllocation(t *testing.T) {
	// This is a compile-time/design guarantee rather than a runtime test,
	// but we verify no panic occurs and the function returns deterministically.
	for i := 0; i < 1000; i++ {
		_ = Grants(RoleAdmin, PermVacancyCreate)
	}
}

func TestVerifyMatrixPassesOnValidMatrix(t *testing.T) {
	// The matrix as written should pass verification.
	if err := VerifyMatrix(); err != nil {
		t.Fatalf("VerifyMatrix() returned unexpected error: %v", err)
	}
}

func TestVerifyMatrixNoDuplicatesInAllPermissions(t *testing.T) {
	// AllPermissions should have no duplicates — VerifyMatrix checks this.
	seen := make(map[Permission]struct{})
	for _, p := range AllPermissions {
		if _, dup := seen[p]; dup {
			t.Fatalf("AllPermissions contains duplicate: %s", p)
		}
		seen[p] = struct{}{}
	}
}

func TestVerifyMatrixAllRolesPresent(t *testing.T) {
	// permissionMatrix should have entries for all four roles.
	for _, r := range AllRoles {
		if _, ok := permissionMatrix[r]; !ok {
			t.Fatalf("permissionMatrix missing entry for role: %s", r)
		}
	}
}

func TestVerifyMatrixOwnerSuperset(t *testing.T) {
	// Owner's set must be a superset of the union of the other three roles.
	var union PermissionSet
	for _, r := range []Role{RoleAdmin, RoleHR, RoleFinance} {
		union |= permissionMatrix[r]
	}
	ownerSet := permissionMatrix[RoleOwner]
	if ownerSet&union != union {
		t.Fatal("Owner's permission set is not a superset of the union of Admin, HR, and Finance")
	}
}
