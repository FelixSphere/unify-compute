package accounts

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

func (f *fixture) acceptInto(t *testing.T, owner User, email, role string) User {
	t.Helper()
	ctx := context.Background()
	_, token, err := f.svc.Invite(ctx, owner.AccountID, owner.ID, email, role)
	if err != nil {
		t.Fatalf("invite %s: %v", email, err)
	}
	h, _ := f.svc.HashPassword(ctx, testPassword)
	u, _, err := f.svc.AcceptInvitation(ctx, token, h)
	if err != nil {
		t.Fatalf("accept %s: %v", email, err)
	}
	return u
}

func TestSignupNamesTheAccountAfterItsOwner(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	u, a := f.signup(t, "First@Example.com")
	if a.Name != "first@example.com" || u.Role != RoleOwner || a.MemberLimit != 20 {
		t.Fatalf("account %+v user role %q", a, u.Role)
	}
}

// Two owners demoting each other at once must not leave the account with
// none: the account row lock serialises them.
func TestConcurrentDemotionsKeepAnOwner(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a := f.activate(t, "a@example.com")
	b := f.acceptInto(t, a, "b@example.com", RoleAdmin)
	if _, _, err := f.svc.ChangeRole(context.Background(), a.AccountID, b.ID, RoleOwner); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i, id := range []int64{a.ID, b.ID} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _, errs[i] = f.svc.ChangeRole(context.Background(), a.AccountID, id, RoleMember)
		}()
	}
	wg.Wait()
	var refused int
	for _, err := range errs {
		if errors.Is(err, ErrLastOwner) {
			refused++
		} else if err != nil {
			t.Fatal(err)
		}
	}
	var owners int
	if err := f.db.QueryRow(context.Background(), `SELECT count(*) FROM users WHERE account_id = $1 AND role = 'owner'`, a.AccountID).Scan(&owners); err != nil {
		t.Fatal(err)
	}
	if refused != 1 || owners != 1 {
		t.Fatalf("refused %d, owners left %d", refused, owners)
	}
}

func TestConcurrentInvitesRespectTheCap(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a := f.activate(t, "a@example.com")
	if _, err := f.db.Exec(context.Background(), `UPDATE accounts SET member_limit = 3 WHERE id = $1`, a.AccountID); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errs := make([]error, 6)
	for i := range errs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _, errs[i] = f.svc.Invite(context.Background(), a.AccountID, a.ID, fmt.Sprintf("p%d@example.com", i), RoleMember)
		}()
	}
	wg.Wait()
	var ok, capped int
	for _, err := range errs {
		var ae *Error
		switch {
		case err == nil:
			ok++
		case errors.As(err, &ae) && ae.Code == "member_limit_reached":
			capped++
		default:
			t.Fatal(err)
		}
	}
	if ok != 2 || capped != 4 {
		t.Fatalf("%d invited, %d capped", ok, capped)
	}
}

func TestInvitationTokenLifecycle(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	a := f.activate(t, "a@example.com")
	first, t1, err := f.svc.Invite(ctx, a.AccountID, a.ID, "x@example.com", RoleMember)
	if err != nil {
		t.Fatal(err)
	}
	var stored []byte
	if err := f.db.QueryRow(ctx, `SELECT token_digest FROM invitations WHERE id = $1`, first.ID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if string(stored) == t1 || string(stored) != string(digest(t1)) {
		t.Fatal("the token is not stored as its SHA-256")
	}
	if _, err := f.svc.PeekInvitation(ctx, t1); err != nil {
		t.Fatalf("peek: %v", err)
	}
	// Peeking does not use it; reissuing does.
	second, t2, err := f.svc.ReissueInvitation(ctx, a.AccountID, a.ID, first.ID)
	if err != nil || second.ID == first.ID || t2 == t1 {
		t.Fatalf("reissue %+v %v", second, err)
	}
	if _, err := f.svc.PeekInvitation(ctx, t1); !errors.Is(err, ErrInvitationInvalid) {
		t.Fatalf("superseded token peeks as %v", err)
	}
	f.clock.advance(InvitationTTL)
	if _, err := f.svc.PeekInvitation(ctx, t2); !errors.Is(err, ErrInvitationInvalid) {
		t.Fatalf("expired token peeks as %v", err)
	}
	// Kept for "Send again" for a while, then purged.
	if l, _ := f.svc.ListInvitations(ctx, a.AccountID); len(l) != 1 {
		t.Fatalf("listed %d", len(l))
	}
	f.clock.advance(InvitationShownFor + time.Second)
	if err := f.svc.PurgeExpired(ctx); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := f.db.QueryRow(ctx, `SELECT count(*) FROM invitations WHERE account_id = $1`, a.AccountID).Scan(&n); err != nil || n != 0 {
		t.Fatalf("%d invitations after the purge (%v)", n, err)
	}
}

func TestRemovingTombstonesTheAddress(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	a := f.activate(t, "a@example.com")
	m := f.acceptInto(t, a, "m@example.com", RoleMember)
	if _, err := f.svc.CreateSession(ctx, m.ID, ClientInfo{}); err != nil {
		t.Fatal(err)
	}
	removed, err := f.svc.RemoveMember(ctx, a.AccountID, m.ID)
	if err != nil || removed.Email != "m@example.com" {
		t.Fatalf("remove %+v %v", removed, err)
	}
	got, err := f.svc.UserByID(ctx, m.ID)
	if err != nil || got.Status != UserClosed || got.Email != fmt.Sprintf("closed+%d@invalid", m.ID) {
		t.Fatalf("after removal %+v %v", got, err)
	}
	var sessions int
	_ = f.db.QueryRow(ctx, `SELECT count(*) FROM sessions WHERE user_id = $1`, m.ID).Scan(&sessions)
	if sessions != 0 {
		t.Fatalf("%d sessions left", sessions)
	}
	if _, err := f.svc.RemoveMember(ctx, a.AccountID, a.ID); !errors.Is(err, ErrLastOwner) {
		t.Fatalf("removing the last owner: %v", err)
	}
}

func TestRolesAndNames(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		actor, target string
		self, want    bool
	}{
		{RoleOwner, RoleOwner, false, true},
		{RoleAdmin, RoleMember, false, true},
		{RoleAdmin, RoleAdmin, false, false},
		{RoleAdmin, RoleOwner, false, false},
		{RoleMember, RoleMember, false, false},
		{RoleMember, RoleMember, true, true},
	} {
		actor := User{ID: 1, Role: c.actor}
		target := User{ID: 2, Role: c.target}
		if c.self {
			target.ID = 1
		}
		if got := CanRemove(actor, target); got != c.want {
			t.Errorf("%s removes %s (self %v): %v", c.actor, c.target, c.self, got)
		}
	}
	if CanInviteAs(RoleAdmin, RoleAdmin) || !CanInviteAs(RoleAdmin, RoleMember) || !CanInviteAs(RoleOwner, RoleAdmin) || CanInviteAs(RoleMember, RoleMember) {
		t.Error("CanInviteAs")
	}
	for _, bad := range []string{"", "   ", "a\nb", "<x>", "a\u202eb", strings.Repeat("x", 65)} {
		if _, err := ValidateAccountName(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	if n, err := ValidateAccountName("  Acme Research  "); err != nil || n != "Acme Research" {
		t.Errorf("%q %v", n, err)
	}
}
