package auth

import (
	"errors"
	"os/user"
	"strings"
	"testing"
)

// fakeUsers wires a GroupGate to an in-memory user database:
// username → (uid, group names). Fake gids carry the group name
// ("gid-wheel") so lookupGroup can invert them without extra state.
func fakeUsers(g *GroupGate, users map[string]struct {
	UID    string
	Groups []string
}) {
	g.lookupUser = func(name string) (*user.User, error) {
		u, ok := users[name]
		if !ok {
			return nil, user.UnknownUserError(name)
		}
		return &user.User{Uid: u.UID, Username: name}, nil
	}
	g.groupIDs = func(u *user.User) ([]string, error) {
		var ids []string
		for _, name := range users[u.Username].Groups {
			ids = append(ids, "gid-"+name)
		}
		return ids, nil
	}
	g.lookupGroup = func(gid string) (*user.Group, error) {
		return &user.Group{Gid: gid, Name: strings.TrimPrefix(gid, "gid-")}, nil
	}
}

func TestGroupGate(t *testing.T) {
	g := NewGroupGate([]string{"wheel", "nixbox-admins"})
	fakeUsers(g, map[string]struct {
		UID    string
		Groups []string
	}{
		"root":  {UID: "0", Groups: nil},
		"alice": {UID: "1000", Groups: []string{"users", "wheel"}},
		"bob":   {UID: "1001", Groups: []string{"users"}},
	})

	// root is always authorized, groups or not.
	if err := g.Authorize("root"); err != nil {
		t.Errorf("root: %v", err)
	}
	// A member of any allowed group passes.
	if err := g.Authorize("alice"); err != nil {
		t.Errorf("alice (wheel): %v", err)
	}
	// A valid user outside the allowed groups is rejected with the
	// distinct not-authorized error, not a credentials failure.
	if err := g.Authorize("bob"); !errors.Is(err, ErrNotAuthorized) {
		t.Errorf("bob: err = %v, want ErrNotAuthorized", err)
	}
	// An unknown user is a system-level error (PAM said yes, passwd says
	// no — misconfiguration, not a policy denial).
	if err := g.Authorize("ghost"); err == nil || errors.Is(err, ErrNotAuthorized) {
		t.Errorf("ghost: err = %v, want lookup error", err)
	}
}

func TestGroupGateLookupFaults(t *testing.T) {
	g := NewGroupGate([]string{"wheel"})
	boom := errors.New("nss exploded")

	g.lookupUser = func(string) (*user.User, error) {
		return &user.User{Uid: "1000", Username: "alice"}, nil
	}
	g.groupIDs = func(*user.User) ([]string, error) { return nil, boom }
	if err := g.Authorize("alice"); !errors.Is(err, boom) {
		t.Errorf("groupIDs fault: %v", err)
	}

	// A group id that cannot be resolved is skipped, not fatal: NSS
	// sources can list stale gids, and membership in the *other* groups
	// still decides the outcome.
	g.groupIDs = func(*user.User) ([]string, error) { return []string{"100", "1"}, nil }
	g.lookupGroup = func(gid string) (*user.Group, error) {
		if gid == "100" {
			return nil, user.UnknownGroupIdError(gid)
		}
		return &user.Group{Gid: gid, Name: "wheel"}, nil
	}
	if err := g.Authorize("alice"); err != nil {
		t.Errorf("stale gid should be skipped: %v", err)
	}

	// ...but if no resolvable group matches, it is still a denial.
	g.lookupGroup = func(gid string) (*user.Group, error) {
		return nil, user.UnknownGroupIdError(gid)
	}
	if err := g.Authorize("alice"); !errors.Is(err, ErrNotAuthorized) {
		t.Errorf("all gids stale: err = %v, want ErrNotAuthorized", err)
	}
}

// TestGroupGateRealLookups exercises the default os/user wiring against
// the test machine's own account database: the current user exists, and
// authorizing them against their real group list must not error (the
// outcome depends on the machine, so only the error legs are asserted).
func TestGroupGateRealLookups(t *testing.T) {
	me, err := user.Current()
	if err != nil {
		t.Skip("no current user")
	}
	ids, err := me.GroupIds()
	if err != nil || len(ids) == 0 {
		t.Skip("no group ids for current user")
	}
	grp, err := user.LookupGroupId(ids[0])
	if err != nil {
		t.Skip("cannot resolve own group")
	}

	// Allowed = one of the user's real groups → authorized.
	if err := NewGroupGate([]string{grp.Name}).Authorize(me.Username); err != nil && me.Uid != "0" {
		t.Errorf("own group: %v", err)
	}
	// Allowed = a group that does not exist → denied (unless root).
	err = NewGroupGate([]string{"nixbox-no-such-group"}).Authorize(me.Username)
	if me.Uid == "0" {
		if err != nil {
			t.Errorf("root bypass: %v", err)
		}
	} else if !errors.Is(err, ErrNotAuthorized) {
		t.Errorf("err = %v, want ErrNotAuthorized", err)
	}
}
