package auth

import (
	"fmt"
	"os/user"
)

// GroupGate authorizes users by Unix group membership: root always
// passes, everyone else needs to be in at least one of Groups. The
// lookups go through os/user (NSS-aware when built with cgo, /etc files
// otherwise — both correct on NixOS, where declared users materialize in
// /etc/passwd and /etc/group).
type GroupGate struct {
	Groups []string

	// Swappable for tests; NewGroupGate wires the real os/user lookups.
	lookupUser  func(name string) (*user.User, error)
	groupIDs    func(u *user.User) ([]string, error)
	lookupGroup func(gid string) (*user.Group, error)
}

func NewGroupGate(groups []string) *GroupGate {
	return &GroupGate{
		Groups:      groups,
		lookupUser:  user.Lookup,
		groupIDs:    (*user.User).GroupIds,
		lookupGroup: user.LookupGroupId,
	}
}

func (g *GroupGate) Authorize(username string) error {
	u, err := g.lookupUser(username)
	if err != nil {
		// PAM accepted the login but the account database can't resolve
		// it — a system problem, not a policy denial.
		return fmt.Errorf("looking up %q: %w", username, err)
	}
	if u.Uid == "0" {
		return nil
	}
	ids, err := g.groupIDs(u)
	if err != nil {
		return fmt.Errorf("groups of %q: %w", username, err)
	}
	for _, gid := range ids {
		grp, err := g.lookupGroup(gid)
		if err != nil {
			// Stale gids happen (group removed while memberships
			// linger); the remaining groups still decide.
			continue
		}
		for _, allowed := range g.Groups {
			if grp.Name == allowed {
				return nil
			}
		}
	}
	return fmt.Errorf("%q: %w", username, ErrNotAuthorized)
}
