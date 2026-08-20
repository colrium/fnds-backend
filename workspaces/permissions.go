package workspaces

// Role is one of the four Workspace_Roles (Req 7.1).
type Role string

const (
	RoleOwner   Role = "owner"
	RoleAdmin   Role = "admin"
	RoleHR      Role = "hr"
	RoleFinance Role = "finance"
)

// AllRoles is the closed set of Req 7.1. Order is stable so table tests and the
// matrix endpoint are deterministic.
var AllRoles = [...]Role{RoleOwner, RoleAdmin, RoleHR, RoleFinance}

// Permission is a named capability checked before a workspace-scoped action proceeds.
type Permission string

const (
	PermWorkspaceView               Permission = "workspace.view"
	PermWorkspaceRename             Permission = "workspace.rename"
	PermWorkspaceDelete             Permission = "workspace.delete"
	PermWorkspaceImportPersonalData Permission = "workspace.import_personal_data"
	PermOwnershipTransfer           Permission = "ownership.transfer"
	PermMemberView                  Permission = "member.view"
	PermMemberInvite                Permission = "member.invite"
	PermMemberRoleChange            Permission = "member.role_change"
	PermMemberRemove                Permission = "member.remove"
	PermVacancyCreate               Permission = "vacancy.create"
	PermVacancyUpdate               Permission = "vacancy.update"
	PermVacancyClose                Permission = "vacancy.close"
	PermVacancyView                 Permission = "vacancy.view"
	PermApplicationView             Permission = "application.view"
	PermApplicationDecide           Permission = "application.decide"
	PermCompanyCreate               Permission = "company.create"
	PermCompanyUpdate               Permission = "company.update"
	PermArtisanBrowse               Permission = "artisan.browse"
	PermBillingView                 Permission = "billing.view"
	PermBillingManage               Permission = "billing.manage"
	PermPaymentMethodManage         Permission = "payment_method.manage"
	PermInvoiceView                 Permission = "invoice.view"
	PermInvoicePay                  Permission = "invoice.pay"
	PermPayoutView                  Permission = "payout.view"
	PermSubscriptionManage          Permission = "subscription.manage"
	PermAuditView                   Permission = "audit.view"
)

// AllPermissions is the universe Req 7.3 quantifies over: Owner holds exactly
// this set. Adding a Permission constant without adding it here is caught by a
// startup check and by P1, so the two lists cannot silently diverge.
var AllPermissions = [...]Permission{
	PermWorkspaceView, PermWorkspaceRename, PermWorkspaceDelete,
	PermWorkspaceImportPersonalData, PermOwnershipTransfer,
	PermMemberView, PermMemberInvite, PermMemberRoleChange, PermMemberRemove,
	PermVacancyCreate, PermVacancyUpdate, PermVacancyClose, PermVacancyView,
	PermApplicationView, PermApplicationDecide,
	PermCompanyCreate, PermCompanyUpdate, PermArtisanBrowse,
	PermBillingView, PermBillingManage, PermPaymentMethodManage,
	PermInvoiceView, PermInvoicePay, PermPayoutView, PermSubscriptionManage,
	PermAuditView,
}

// PermissionSet is a bitmask over the index of each Permission in
// AllPermissions. 26 permissions fit in a uint32 with room to spare, and a
// bitmask makes Grants a single AND — no map lookup, no allocation, and no
// database handle in scope, which is how Req 7.13 is enforced by signature
// rather than by review.
type PermissionSet uint32

// permBit holds the precomputed single-bit mask for each Permission, indexed
// by its position in AllPermissions.
var permBit [len(AllPermissions)]PermissionSet

// permissionIndex maps each Permission to its bit position in AllPermissions.
var permissionIndex map[Permission]uint

// permissionMatrix is the Permission_Matrix. It is the only place a role's
// authority is written down (Req 7.13, 7.18).
var permissionMatrix map[Role]PermissionSet

// roleSets is an array indexed by roleIdx values, holding the precomputed
// PermissionSet for each role. Grants uses this to perform a single AND with
// no allocation and no database handle in scope.
// Index 0 is unused (zero set for unknown roles).
var roleSets [5]PermissionSet

// roleIdx maps each Role to its index in roleSets.
var roleIdx map[Role]uint8

func init() {
	// Step 1: Build the permission index and bit masks.
	permissionIndex = make(map[Permission]uint, len(AllPermissions))
	for i, p := range AllPermissions {
		permissionIndex[p] = uint(i)
		permBit[i] = 1 << uint(i)
	}

	// Step 2: Build the permission matrix now that permissionIndex is populated.
	permissionMatrix = map[Role]PermissionSet{
		// Req 7.3: Owner holds every Permission the backend defines.
		RoleOwner: setOf(AllPermissions[:]...),

		// Req 7.4, verbatim. PermInvoiceView and PermPayoutView are Admin's
		// read-only money access; the four money *write* permissions are not here,
		// which is what keeps Req 7.9 true.
		RoleAdmin: setOf(
			PermWorkspaceView, PermWorkspaceRename,
			PermMemberView, PermMemberInvite, PermMemberRoleChange, PermMemberRemove,
			PermVacancyCreate, PermVacancyUpdate, PermVacancyClose, PermVacancyView,
			PermApplicationView, PermApplicationDecide,
			PermCompanyCreate, PermCompanyUpdate, PermArtisanBrowse,
			PermBillingView, PermInvoiceView, PermPayoutView, PermAuditView,
		),

		// Req 7.5, verbatim.
		RoleHR: setOf(
			PermWorkspaceView, PermMemberView,
			PermVacancyCreate, PermVacancyUpdate, PermVacancyClose, PermVacancyView,
			PermApplicationView, PermApplicationDecide,
			PermCompanyCreate, PermCompanyUpdate, PermArtisanBrowse,
		),

		// Req 7.6, verbatim.
		RoleFinance: setOf(
			PermWorkspaceView, PermMemberView,
			PermBillingView, PermBillingManage, PermPaymentMethodManage,
			PermInvoiceView, PermInvoicePay, PermPayoutView, PermSubscriptionManage,
		),
	}

	// Step 3: Build the role lookup array for the Grants hot path.
	roleIdx = map[Role]uint8{
		RoleOwner:   1,
		RoleAdmin:   2,
		RoleHR:      3,
		RoleFinance: 4,
	}
	for r, s := range permissionMatrix {
		roleSets[roleIdx[r]] = s
	}
}

// setOf builds a PermissionSet from a list of Permissions.
func setOf(ps ...Permission) PermissionSet {
	var s PermissionSet
	for _, p := range ps {
		if idx, ok := permissionIndex[p]; ok {
			s |= 1 << idx
		}
	}
	return s
}

// has returns true when the PermissionSet includes the given Permission.
func (s PermissionSet) has(p Permission) bool {
	idx, ok := permissionIndex[p]
	if !ok {
		return false
	}
	return s&permBit[idx] != 0
}

// Grants reports whether the given Role holds the given Permission. It performs
// a single AND with no database handle in scope and no allocation. An unknown
// role yields the empty set rather than a panic (Req 7.13, 7.14).
func Grants(r Role, p Permission) bool {
	idx, ok := permissionIndex[p]
	if !ok {
		return false
	}
	return roleSets[roleIdx[r]]&permBit[idx] != 0
}

// RolesWithPermission returns every Role that holds the given Permission, in
// the stable order of AllRoles. Callers that need a set of roles for a
// specific permission (e.g. building a query filter) should use this instead
// of hardcoding role names, so the permission matrix stays the single source
// of truth.
//
// Its only caller is GetApplicationNotifyMembers in application_scope.go, which
// is itself deferred scaffolding for spec task 13.3. A dead-code scan will flag
// this function until that task ships — that is expected, not rot.
func RolesWithPermission(p Permission) []Role {
	idx, ok := permissionIndex[p]
	if !ok {
		return nil
	}
	var result []Role
	for _, r := range AllRoles {
		if roleSets[roleIdx[r]]&permBit[idx] != 0 {
			result = append(result, r)
		}
	}
	return result
}

// GrantedPermissions returns the list of Permissions granted to the given Role,
// in the stable order of AllPermissions. Useful for the API payload (Req 7.16).
func GrantedPermissions(r Role) []Permission {
	s := permissionMatrix[r]
	if s == 0 {
		return nil
	}
	result := make([]Permission, 0, 26)
	for i, p := range AllPermissions {
		if s&permBit[i] != 0 {
			result = append(result, p)
		}
	}
	return result
}

// ValidRole reports whether the given string is one of the four accepted
// Workspace_Role values (Req 7.17).
func ValidRole(s string) bool {
	for _, r := range AllRoles {
		if string(r) == s {
			return true
		}
	}
	return false
}
