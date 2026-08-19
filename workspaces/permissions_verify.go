package workspaces

import "fmt"

// VerifyMatrix is called at startup and returns an error that makes main.go
// exit — a matrix typo is a deployment-blocking bug, not a runtime surprise.
// It asserts:
// 1. Every Permission constant appears in AllPermissions exactly once
// 2. permissionMatrix has an entry for all four roles
// 3. Owner's set is a superset of the union of the other three
func VerifyMatrix() error {
	// 1. Check AllPermissions has no duplicates.
	seen := make(map[Permission]struct{}, len(AllPermissions))
	for _, p := range AllPermissions {
		if _, dup := seen[p]; dup {
			return fmt.Errorf("VerifyMatrix: duplicate permission in AllPermissions: %s", p)
		}
		seen[p] = struct{}{}
	}

	// 2. Check permissionMatrix has entries for all four roles.
	for _, r := range AllRoles {
		if _, ok := permissionMatrix[r]; !ok {
			return fmt.Errorf("VerifyMatrix: permissionMatrix missing entry for role %s", r)
		}
	}

	// 3. Compute the union of Admin, HR, and Finance permission sets.
	var union PermissionSet
	for _, r := range []Role{RoleAdmin, RoleHR, RoleFinance} {
		union |= permissionMatrix[r]
	}

	// 4. Verify Owner's set is a superset of that union (Owner & union == union).
	ownerSet := permissionMatrix[RoleOwner]
	if ownerSet&union != union {
		// Find the permissions that are in the union but not in Owner's set
		// to produce a descriptive error message.
		missing := union &^ ownerSet
		var missingPerms []Permission
		for i, p := range AllPermissions {
			if missing&permBit[i] != 0 {
				missingPerms = append(missingPerms, p)
			}
		}
		return fmt.Errorf(
			"VerifyMatrix: Owner's permission set is not a superset of the union of Admin, HR, and Finance; missing: %v",
			missingPerms,
		)
	}

	return nil
}
