package dashboard

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/FelixSphere/unify-compute/compute-api/internal/accounts"
	"github.com/FelixSphere/unify-compute/compute-api/internal/mailer"
	"github.com/FelixSphere/unify-compute/compute-api/internal/notify"
)

// The acceptance list of docs/plan-own-accounts.md A5b and spec/accounts.md
// "Members and roles".

type meBody struct {
	User struct {
		Email string `json:"email"`
		Role  string `json:"role"`
	} `json:"user"`
	Account struct {
		ID   int64  `json:"id"`
		Name string `json:"name"`
	} `json:"account"`
}

func (f *fixture) me(s session) meBody {
	f.t.Helper()
	rec := f.do(f.sessionReq(http.MethodGet, "/me", s, nil))
	if rec.Code != http.StatusOK {
		f.t.Fatalf("/me %d %s", rec.Code, rec.Body)
	}
	var m meBody
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		f.t.Fatal(err)
	}
	return m
}

type invBody struct {
	ID, Email, Role, Status string
	InvitedByEmail          string `json:"invited_by_email"`
	ExpiresAt               int64  `json:"expires_at"`
}

func (f *fixture) invite(s session, email, role string) invBody {
	f.t.Helper()
	rec := f.do(f.sessionReq(http.MethodPost, "/members/invitations", s, map[string]string{"email": email, "role": role}))
	if rec.Code != http.StatusCreated {
		f.t.Fatalf("invite %s: %d %s", email, rec.Code, rec.Body)
	}
	var b invBody
	if err := json.Unmarshal(rec.Body.Bytes(), &b); err != nil {
		f.t.Fatal(err)
	}
	return b
}

// latestInviteToken returns the token of the newest invitation sent to addr.
func (f *fixture) latestInviteToken(addr string) string {
	f.t.Helper()
	f.h.Wait()
	msgs := f.mail.to(addr)
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].kind == "invitation" {
			if m := tokenRe.FindStringSubmatch(msgs[i].text); m != nil {
				tok, err := url.QueryUnescape(m[1])
				if err != nil {
					f.t.Fatal(err)
				}
				return tok
			}
		}
	}
	f.t.Fatalf("no invitation sent to %s", addr)
	return ""
}

func acceptBody(token string) map[string]string {
	return map[string]string{"token": token, "password": goodPass, "password_confirm": goodPass}
}

// join invites addr into owner's account with role and accepts it.
func (f *fixture) join(owner session, addr, role string) session {
	f.t.Helper()
	f.invite(owner, addr, role)
	rec := f.do(post("/invitations/accept", acceptBody(f.latestInviteToken(addr))))
	if rec.Code != http.StatusOK {
		f.t.Fatalf("accept %s: %d %s", addr, rec.Code, rec.Body)
	}
	return sessionFrom(f.t, rec)
}

func (f *fixture) members(s session) []memberView {
	f.t.Helper()
	rec := f.do(f.sessionReq(http.MethodGet, "/members", s, nil))
	if rec.Code != http.StatusOK {
		f.t.Fatalf("/members %d %s", rec.Code, rec.Body)
	}
	var out []memberView
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		f.t.Fatal(err)
	}
	return out
}

func (f *fixture) userID(email string) int64 {
	f.t.Helper()
	u, err := f.acc.UserByEmail(context.Background(), email)
	if err != nil {
		f.t.Fatal(err)
	}
	return u.ID
}

func (f *fixture) auditActors(accountID int64, action string) []string {
	f.t.Helper()
	rows, err := f.db.Query(context.Background(), `SELECT actor_id FROM audit_events WHERE account_id = $1 AND action = $2 ORDER BY seq`, accountID, action)
	if err != nil {
		f.t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var a string
		if err := rows.Scan(&a); err != nil {
			f.t.Fatal(err)
		}
		out = append(out, a)
	}
	return out
}

func TestInviteAndAcceptWithoutAVerificationCode(t *testing.T) {
	f := newFixture(t)
	owner := f.activeUser("grace@example.com")
	m := f.me(owner)
	if m.User.Role != "owner" || m.Account.Name != "grace@example.com" {
		t.Fatalf("/me after signup: %+v", m)
	}
	inv := f.invite(owner, " Ada@Example.com ", "member")
	if inv.Email != "ada@example.com" || inv.Role != "member" || inv.Status != "pending" || inv.InvitedByEmail != "grace@example.com" ||
		inv.ExpiresAt != f.clock.now().Add(7*24*time.Hour).Unix() {
		t.Fatalf("invitation %+v", inv)
	}
	token := f.latestInviteToken("ada@example.com")

	rec := f.do(post("/invitations/lookup", map[string]string{"token": token}))
	if rec.Code != http.StatusOK {
		t.Fatalf("lookup %d %s", rec.Code, rec.Body)
	}
	var look map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &look)
	if look["account_name"] != "grace@example.com" || look["email"] != "ada@example.com" || look["role"] != "member" || look["invited_by_email"] != "grace@example.com" {
		t.Fatalf("lookup %v", look)
	}

	rec = f.do(post("/invitations/accept", acceptBody(token)))
	if rec.Code != http.StatusOK {
		t.Fatalf("accept %d %s", rec.Code, rec.Body)
	}
	ada := sessionFrom(t, rec)
	am := f.me(ada)
	if am.User.Role != "member" || am.Account.ID != m.Account.ID {
		t.Fatalf("/me of the invitee: %+v, owner's account %d", am, m.Account.ID)
	}
	for _, mm := range f.mail.to("ada@example.com") {
		if mm.kind == "verification-code" {
			t.Fatal("a verification code was sent to an invited address")
		}
	}
	f.h.Wait()
	if got := f.mail.to("grace@example.com"); len(got) == 0 || got[len(got)-1].kind != "invitation-accepted" {
		t.Fatalf("inviter not told: %+v", got)
	}

	// Single use.
	wantErr(t, f.do(post("/invitations/lookup", map[string]string{"token": token})), 404, "not_found", "not_found")
	wantErr(t, f.do(post("/invitations/accept", acceptBody(token))), 404, "not_found", "not_found")

	// The invitee's key draws on the same account.
	k := f.createKey(ada, map[string]any{"name": "ada laptop", "scopes": []string{"jobs"}})
	vk, err := f.acc.VerifyKey(context.Background(), k.Secret)
	if err != nil || vk.AccountID != m.Account.ID || accounts.OwnerFor(vk.AccountID) != "account:"+strconv.FormatInt(m.Account.ID, 10) {
		t.Fatalf("invitee key resolves to %+v %v", vk, err)
	}

	list := f.members(ada)
	if len(list) != 2 || list[0].Email != "grace@example.com" || list[0].Role != "owner" || list[1].Email != "ada@example.com" {
		t.Fatalf("members %+v", list)
	}
	grace := strconv.FormatInt(f.userID("grace@example.com"), 10)
	if a := f.auditActors(m.Account.ID, accounts.ActionInvited); !slices.Equal(a, []string{grace}) {
		t.Fatalf("invited actors %v", a)
	}
	if a := f.auditActors(m.Account.ID, accounts.ActionInviteAccepted); len(a) != 1 || a[0] != strconv.FormatInt(f.userID("ada@example.com"), 10) {
		t.Fatalf("accepted actors %v", a)
	}
}

func TestAcceptValidatesThePassword(t *testing.T) {
	f := newFixture(t)
	owner := f.activeUser("owner@example.com")
	f.invite(owner, "new@example.com", "member")
	token := f.latestInviteToken("new@example.com")
	wantErr(t, f.do(post("/invitations/accept", map[string]string{"token": token, "password": goodPass, "password_confirm": "other"})),
		400, "invalid_request", "password_mismatch")
	wantErr(t, f.do(post("/invitations/accept", map[string]string{"token": token, "password": "new@example.com", "password_confirm": "new@example.com"})),
		400, "invalid_request", "weak_password")
	// Still usable after refusals.
	if rec := f.do(post("/invitations/accept", acceptBody(token))); rec.Code != http.StatusOK {
		t.Fatalf("accept %d %s", rec.Code, rec.Body)
	}
}

func TestMembersCannotManageOrTopUp(t *testing.T) {
	f := newFixture(t)
	owner := f.activeUser("owner@example.com")
	member := f.join(owner, "member@example.com", "member")

	for _, r := range []req{
		f.sessionReq(http.MethodPost, "/members/invitations", member, map[string]string{"email": "x@example.com", "role": "member"}),
		f.sessionReq(http.MethodGet, "/members/invitations", member, nil),
		f.sessionReq(http.MethodPost, "/topups", member, map[string]string{"amount_usd": "20.00"}),
		f.sessionReq(http.MethodPatch, "/balance", member, map[string]string{"low_balance_usd": "1.00"}),
		f.sessionReq(http.MethodPatch, "/account", member, map[string]string{"name": "Mine now"}),
		f.sessionReq(http.MethodPatch, "/members/"+strconv.FormatInt(f.userID("owner@example.com"), 10), member, map[string]string{"role": "member"}),
		f.sessionReq(http.MethodDelete, "/members/"+strconv.FormatInt(f.userID("owner@example.com"), 10), member, nil),
	} {
		t.Run(r.method+" "+r.path, func(t *testing.T) {
			wantErr(t, f.do(r), 403, "permission_error", "role_required")
		})
	}
	// Any role reads the member list.
	if n := len(f.members(member)); n != 2 {
		t.Fatalf("%d members", n)
	}
}

func TestMemberRevokesOnlyItsOwnKeys(t *testing.T) {
	f := newFixture(t)
	owner := f.activeUser("owner@example.com")
	member := f.join(owner, "member@example.com", "member")
	ownerKey := f.createKey(owner, map[string]any{"name": "owner", "scopes": []string{"jobs"}})
	memberKey := f.createKey(member, map[string]any{"name": "member", "scopes": []string{"jobs"}})

	wantErr(t, f.do(f.sessionReq(http.MethodDelete, fmt.Sprintf("/keys/%d", ownerKey.ID), member, nil)), 403, "permission_error", "role_required")
	if rec := f.do(f.sessionReq(http.MethodDelete, fmt.Sprintf("/keys/%d", memberKey.ID), member, nil)); rec.Code != http.StatusNoContent {
		t.Fatalf("member revokes own key: %d %s", rec.Code, rec.Body)
	}
	other := f.activeUser("other@example.com")
	otherKey := f.createKey(other, map[string]any{"name": "other", "scopes": []string{"jobs"}})
	wantErr(t, f.do(f.sessionReq(http.MethodDelete, fmt.Sprintf("/keys/%d", otherKey.ID), member, nil)), 404, "not_found", "not_found")

	// An owner revokes anyone's key.
	memberKey2 := f.createKey(member, map[string]any{"name": "member 2", "scopes": []string{"jobs"}})
	if rec := f.do(f.sessionReq(http.MethodDelete, fmt.Sprintf("/keys/%d", memberKey2.ID), owner, nil)); rec.Code != http.StatusNoContent {
		t.Fatalf("owner revokes member key: %d %s", rec.Code, rec.Body)
	}
}

func TestAdminsManageMembersOnly(t *testing.T) {
	f := newFixture(t)
	owner := f.activeUser("owner@example.com")
	admin := f.join(owner, "admin@example.com", "admin")
	f.join(owner, "member@example.com", "member")
	f.join(owner, "admin2@example.com", "admin")

	wantErr(t, f.do(f.sessionReq(http.MethodPost, "/members/invitations", admin, map[string]string{"email": "a3@example.com", "role": "admin"})),
		403, "permission_error", "role_required")
	f.invite(admin, "m2@example.com", "member")
	wantErr(t, f.do(f.sessionReq(http.MethodDelete, "/members/"+strconv.FormatInt(f.userID("admin2@example.com"), 10), admin, nil)),
		403, "permission_error", "role_required")
	wantErr(t, f.do(f.sessionReq(http.MethodPatch, "/members/"+strconv.FormatInt(f.userID("member@example.com"), 10), admin, map[string]string{"role": "admin"})),
		403, "permission_error", "role_required")
	if rec := f.do(f.sessionReq(http.MethodDelete, "/members/"+strconv.FormatInt(f.userID("member@example.com"), 10), admin, nil)); rec.Code != http.StatusNoContent {
		t.Fatalf("admin removes member: %d %s", rec.Code, rec.Body)
	}
	rec := f.do(f.sessionReq(http.MethodPatch, "/account", admin, map[string]string{"name": "  Acme Research "}))
	if rec.Code != http.StatusOK || f.me(owner).Account.Name != "Acme Research" {
		t.Fatalf("admin renames: %d %s", rec.Code, rec.Body)
	}
	wantErr(t, f.do(f.sessionReq(http.MethodPatch, "/account", admin, map[string]string{"name": "<b>"})), 400, "invalid_request", "invalid_name")
	wantErr(t, f.do(f.sessionReq(http.MethodPatch, "/account", admin, map[string]string{"name": strings.Repeat("x", 65)})), 400, "invalid_request", "invalid_name")
	if a := f.auditActions(f.accountOf("owner@example.com")); !slices.Contains(a, accounts.ActionAccountRenamed) || !slices.Contains(a, accounts.ActionMemberRemoved) {
		t.Fatalf("audit %v", a)
	}
}

func TestLastOwnerCannotBeDemotedOrRemoved(t *testing.T) {
	f := newFixture(t)
	owner := f.activeUser("owner@example.com")
	f.join(owner, "next@example.com", "member")
	self := "/members/" + strconv.FormatInt(f.userID("owner@example.com"), 10)
	next := "/members/" + strconv.FormatInt(f.userID("next@example.com"), 10)

	wantErr(t, f.do(f.sessionReq(http.MethodPatch, self, owner, map[string]string{"role": "admin"})), 409, "conflict", "last_owner")
	wantErr(t, f.do(f.sessionReq(http.MethodDelete, self, owner, nil)), 409, "conflict", "last_owner")
	wantErr(t, f.do(f.sessionReq(http.MethodPatch, next, owner, map[string]string{"role": "boss"})), 400, "invalid_request", "invalid_role")

	// Ownership changes hands by promoting first.
	if rec := f.do(f.sessionReq(http.MethodPatch, next, owner, map[string]string{"role": "owner"})); rec.Code != http.StatusOK {
		t.Fatalf("promote %d %s", rec.Code, rec.Body)
	}
	if rec := f.do(f.sessionReq(http.MethodPatch, self, owner, map[string]string{"role": "member"})); rec.Code != http.StatusOK {
		t.Fatalf("demote self %d %s", rec.Code, rec.Body)
	}
	// The demoted session loses owner powers on its next request.
	wantErr(t, f.do(f.sessionReq(http.MethodPatch, next, owner, map[string]string{"role": "member"})), 403, "permission_error", "role_required")
	if got := f.auditActions(f.accountOf("owner@example.com")); strings.Count(strings.Join(got, ","), accounts.ActionRoleChanged) != 2 {
		t.Fatalf("audit %v", got)
	}
}

func TestRemovedMemberIsLoggedOutAndTold(t *testing.T) {
	f := newFixture(t)
	owner := f.activeUser("owner@example.com")
	member := f.join(owner, "gone@example.com", "member")
	id := f.userID("gone@example.com")
	key := f.createKey(member, map[string]any{"name": "stays", "scopes": []string{"jobs"}})

	if rec := f.do(f.sessionReq(http.MethodDelete, "/members/"+strconv.FormatInt(id, 10), owner, nil)); rec.Code != http.StatusNoContent {
		t.Fatalf("remove %d %s", rec.Code, rec.Body)
	}
	wantErr(t, f.do(f.sessionReq(http.MethodGet, "/me", member, nil)), 401, "authentication_error", "session_required")
	wantErr(t, f.login("gone@example.com", goodPass), 401, "authentication_error", "invalid_credentials")
	f.h.Wait()
	if got := f.mail.to("gone@example.com"); got[len(got)-1].kind != "member-removed" || !strings.Contains(got[len(got)-1].text, "owner@example.com") {
		t.Fatalf("removal mail %+v", got)
	}
	// The key belongs to the account and keeps working.
	if _, err := f.acc.VerifyKey(context.Background(), key.Secret); err != nil {
		t.Fatalf("key of a removed member: %v", err)
	}
	// The address is free again: it can be invited back.
	f.join(owner, "gone@example.com", "admin")
	wantErr(t, f.do(f.sessionReq(http.MethodDelete, "/members/"+strconv.FormatInt(id, 10), owner, nil)), 404, "not_found", "not_found")
}

func TestAnyoneMayLeave(t *testing.T) {
	f := newFixture(t)
	owner := f.activeUser("owner@example.com")
	member := f.join(owner, "leaver@example.com", "member")
	rec := f.do(f.sessionReq(http.MethodDelete, "/members/"+strconv.FormatInt(f.userID("leaver@example.com"), 10), member, nil))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("leave %d %s", rec.Code, rec.Body)
	}
	if c := sessionCookie(t, rec); c == nil || c.MaxAge >= 0 {
		t.Fatal("cookie not cleared on leaving")
	}
	f.h.Wait()
	if got := f.mail.to("leaver@example.com"); !strings.Contains(got[len(got)-1].text, "You left") {
		t.Fatalf("leave mail %+v", got)
	}
}

func TestInvitationConflicts(t *testing.T) {
	f := newFixture(t)
	owner := f.activeUser("owner@example.com")
	f.join(owner, "member@example.com", "member")
	f.activeUser("elsewhere@example.com")

	e := wantErr(t, f.do(f.sessionReq(http.MethodPost, "/members/invitations", owner, map[string]string{"email": "MEMBER@example.com", "role": "member"})),
		409, "conflict", "already_member")
	if e.Error.Param != "email" {
		t.Fatalf("param %q", e.Error.Param)
	}
	wantErr(t, f.do(f.sessionReq(http.MethodPost, "/members/invitations", owner, map[string]string{"email": "owner@example.com", "role": "admin"})),
		409, "conflict", "already_member")
	wantErr(t, f.do(f.sessionReq(http.MethodPost, "/members/invitations", owner, map[string]string{"email": "x@example.com", "role": "owner"})),
		400, "invalid_request", "invalid_role")
	wantErr(t, f.do(f.sessionReq(http.MethodPost, "/members/invitations", owner, map[string]string{"email": "nope", "role": "member"})),
		400, "invalid_request", "invalid_email")

	// An address on another account can be invited, but not accept: a user
	// belongs to exactly one account.
	f.invite(owner, "elsewhere@example.com", "member")
	token := f.latestInviteToken("elsewhere@example.com")
	wantErr(t, f.do(post("/invitations/accept", acceptBody(token))), 409, "conflict", "email_in_use")
	if rec := f.do(post("/invitations/lookup", map[string]string{"token": token})); rec.Code != http.StatusOK {
		t.Fatal("a refused accept used the invitation up")
	}
}

func TestMemberCapCountsLiveInvitations(t *testing.T) {
	f := newFixture(t)
	owner := f.activeUser("owner@example.com")
	ctx := context.Background()
	var limit int
	if err := f.db.QueryRow(ctx, `SELECT member_limit FROM accounts WHERE id = $1`, f.accountOf("owner@example.com")).Scan(&limit); err != nil || limit != 20 {
		t.Fatalf("default member_limit %d %v", limit, err)
	}
	// 20 invitations a day is also the throttle, so the cap is exercised
	// lower; the 20 above is the same code path.
	if _, err := f.db.Exec(ctx, `UPDATE accounts SET member_limit = 4 WHERE id = $1`, f.accountOf("owner@example.com")); err != nil {
		t.Fatal(err)
	}
	f.join(owner, "m1@example.com", "member")
	f.invite(owner, "p1@example.com", "member")
	f.invite(owner, "p2@example.com", "member")
	wantErr(t, f.do(f.sessionReq(http.MethodPost, "/members/invitations", owner, map[string]string{"email": "p3@example.com", "role": "member"})),
		403, "permission_error", "member_limit_reached")
	// Inviting an address again replaces its invitation; it is not one more.
	f.invite(owner, "p1@example.com", "admin")
	// The test clock does not move, so both share created_at and the order
	// between them is the ids' random tail: look p1 up rather than index.
	list := f.listInvitations(owner)
	i := slices.IndexFunc(list, func(v invBody) bool { return v.Email == "p1@example.com" })
	if len(list) != 2 || i < 0 || list[i].Role != "admin" {
		t.Fatalf("invitations %+v", list)
	}
	// An expired invitation no longer holds a slot.
	f.clock.advance(7*24*time.Hour + time.Second)
	owner = sessionFrom(t, f.login("owner@example.com", goodPass))
	f.invite(owner, "p3@example.com", "member")
	// A withdrawn one frees its slot too.
	f.invite(owner, "p4@example.com", "member")
	wantErr(t, f.do(f.sessionReq(http.MethodPost, "/members/invitations", owner, map[string]string{"email": "p5@example.com", "role": "member"})),
		403, "permission_error", "member_limit_reached")
	var p3 string
	for _, i := range f.listInvitations(owner) {
		if i.Email == "p3@example.com" {
			p3 = i.ID
		}
	}
	if rec := f.do(f.sessionReq(http.MethodDelete, "/members/invitations/"+p3, owner, nil)); rec.Code != http.StatusNoContent {
		t.Fatalf("withdraw %d %s", rec.Code, rec.Body)
	}
	f.invite(owner, "p5@example.com", "member")
}

func TestMemberCapIsTheAccountsLimit(t *testing.T) {
	f := newFixture(t)
	owner := f.activeUser("owner@example.com")
	if _, err := f.db.Exec(context.Background(), `UPDATE accounts SET member_limit = 2 WHERE id = $1`, f.accountOf("owner@example.com")); err != nil {
		t.Fatal(err)
	}
	f.invite(owner, "a@example.com", "member")
	e := wantErr(t, f.do(f.sessionReq(http.MethodPost, "/members/invitations", owner, map[string]string{"email": "b@example.com", "role": "member"})),
		403, "permission_error", "member_limit_reached")
	if !strings.Contains(e.Error.Message, "has 2 members") {
		t.Fatalf("message %q", e.Error.Message)
	}
}

func (f *fixture) listInvitations(s session) []invBody {
	f.t.Helper()
	rec := f.do(f.sessionReq(http.MethodGet, "/members/invitations", s, nil))
	if rec.Code != http.StatusOK {
		f.t.Fatalf("list invitations %d %s", rec.Code, rec.Body)
	}
	var out []invBody
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		f.t.Fatal(err)
	}
	return out
}

func TestExpiredWithdrawnAndResentInvitations(t *testing.T) {
	f := newFixture(t)
	owner := f.activeUser("owner@example.com")
	inv := f.invite(owner, "late@example.com", "member")
	old := f.latestInviteToken("late@example.com")

	f.clock.advance(7*24*time.Hour + time.Second)
	owner = sessionFrom(t, f.login("owner@example.com", goodPass))
	f.clock.advance(accounts.ReauthWindow)
	wantErr(t, f.do(post("/invitations/lookup", map[string]string{"token": old})), 404, "not_found", "not_found")
	wantErr(t, f.do(post("/invitations/accept", acceptBody(old))), 404, "not_found", "not_found")
	list := f.listInvitations(owner)
	if len(list) != 1 || list[0].Status != "expired" {
		t.Fatalf("expired invitation listed as %+v", list)
	}

	// "Send again" needs a fresh password, like inviting.
	wantErr(t, f.do(f.sessionReq(http.MethodPost, "/members/invitations/"+inv.ID+"/resend", owner, nil)), 401, "authentication_error", "reauth_required")
	if rec := f.do(f.sessionReq(http.MethodPost, "/reauth", owner, map[string]string{"password": goodPass})); rec.Code != http.StatusNoContent {
		t.Fatalf("reauth %d", rec.Code)
	}
	rec := f.do(f.sessionReq(http.MethodPost, "/members/invitations/"+inv.ID+"/resend", owner, nil))
	if rec.Code != http.StatusCreated {
		t.Fatalf("resend %d %s", rec.Code, rec.Body)
	}
	var again invBody
	_ = json.Unmarshal(rec.Body.Bytes(), &again)
	if again.ID == inv.ID || again.Status != "pending" || again.Email != "late@example.com" {
		t.Fatalf("resent %+v", again)
	}
	fresh := f.latestInviteToken("late@example.com")
	if fresh == old {
		t.Fatal("resend reused the token")
	}
	// The old id is superseded: it cannot be resent or withdrawn.
	wantErr(t, f.do(f.sessionReq(http.MethodPost, "/members/invitations/"+inv.ID+"/resend", owner, nil)), 404, "not_found", "not_found")

	if rec := f.do(f.sessionReq(http.MethodDelete, "/members/invitations/"+again.ID, owner, nil)); rec.Code != http.StatusNoContent {
		t.Fatalf("withdraw %d %s", rec.Code, rec.Body)
	}
	wantErr(t, f.do(post("/invitations/accept", acceptBody(fresh))), 404, "not_found", "not_found")
	wantErr(t, f.do(f.sessionReq(http.MethodDelete, "/members/invitations/"+again.ID, owner, nil)), 404, "not_found", "not_found")
	if n := len(f.listInvitations(owner)); n != 0 {
		t.Fatalf("%d invitations after withdrawing", n)
	}
	if a := f.auditActions(f.accountOf("owner@example.com")); !slices.Contains(a, accounts.ActionInviteRevoked) {
		t.Fatalf("audit %v", a)
	}
}

func TestMemberActionsNeedAFreshPassword(t *testing.T) {
	f := newFixture(t)
	owner := f.activeUser("owner@example.com")
	f.join(owner, "m@example.com", "member")
	f.clock.advance(accounts.ReauthWindow + time.Second)
	id := strconv.FormatInt(f.userID("m@example.com"), 10)
	for _, r := range []req{
		f.sessionReq(http.MethodPost, "/members/invitations", owner, map[string]string{"email": "x@example.com", "role": "member"}),
		f.sessionReq(http.MethodPatch, "/members/"+id, owner, map[string]string{"role": "admin"}),
		f.sessionReq(http.MethodDelete, "/members/"+id, owner, nil),
		f.sessionReq(http.MethodPatch, "/account", owner, map[string]string{"name": "New"}),
	} {
		wantErr(t, f.do(r), 401, "authentication_error", "reauth_required")
	}
}

func TestInvitationsAreThrottledPerAccount(t *testing.T) {
	f := newFixture(t)
	owner := f.activeUser("owner@example.com")
	f.invite(owner, "a@example.com", "member")
	var last *httptest.ResponseRecorder
	for range 20 {
		last = f.do(f.sessionReq(http.MethodPost, "/members/invitations", owner, map[string]string{"email": "a@example.com", "role": "member"}))
	}
	wantErr(t, last, 429, "rate_limited", "rate_limited")
}

func TestInvitationLinksDoNotCrossAccounts(t *testing.T) {
	f := newFixture(t)
	one := f.activeUser("one@example.com")
	two := f.activeUser("two@example.com")
	inv := f.invite(one, "x@example.com", "member")
	wantErr(t, f.do(f.sessionReq(http.MethodDelete, "/members/invitations/"+inv.ID, two, nil)), 404, "not_found", "not_found")
	wantErr(t, f.do(f.sessionReq(http.MethodDelete, "/members/"+strconv.FormatInt(f.userID("one@example.com"), 10), two, nil)), 404, "not_found", "not_found")
	if n := len(f.listInvitations(two)); n != 0 {
		t.Fatalf("account two sees %d invitations", n)
	}
}

// With the notifier wired (as main does), each member email is recorded in
// notifications and goes out once.
func TestMemberEmailsAreRecordedAndSentOnce(t *testing.T) {
	f := newFixture(t)
	f.h.notes = notify.New(f.db, nil, f.clock.now)
	ctx := context.Background()
	owner := f.activeUser("owner@example.com")
	acct := f.accountOf("owner@example.com")
	inv := f.invite(owner, "m@example.com", "member")
	n := f.h.notes
	if ok, err := n.Sent(ctx, notify.Key{AccountID: acct, Subject: "invitation", SubjectID: inv.ID, Message: mailer.KindInvitation}); err != nil || !ok {
		t.Fatalf("invitation not recorded: %v %v", ok, err)
	}
	rec := f.do(post("/invitations/accept", acceptBody(f.latestInviteToken("m@example.com"))))
	if rec.Code != http.StatusOK {
		t.Fatalf("accept %d %s", rec.Code, rec.Body)
	}
	f.h.Wait()
	if ok, _ := n.Sent(ctx, notify.Key{AccountID: acct, Subject: "invitation", SubjectID: inv.ID, Message: mailer.KindInvitationAccepted}); !ok {
		t.Fatal("accepted email not recorded")
	}
	id := f.userID("m@example.com")
	if rec := f.do(f.sessionReq(http.MethodDelete, "/members/"+strconv.FormatInt(id, 10), owner, nil)); rec.Code != http.StatusNoContent {
		t.Fatalf("remove %d", rec.Code)
	}
	f.h.Wait()
	if ok, _ := n.Sent(ctx, notify.Key{AccountID: acct, Subject: "member", SubjectID: strconv.FormatInt(id, 10), Message: mailer.KindMemberRemoved}); !ok {
		t.Fatal("removal email not recorded")
	}
	removals := 0
	for _, m := range f.mail.to("m@example.com") {
		if m.kind == mailer.KindMemberRemoved {
			removals++
		}
	}
	if removals != 1 {
		t.Fatalf("%d removal emails", removals)
	}
}

// When SES refuses the invitation, the inviter is told and the invitation
// stays, so "Send again" can deliver it.
func TestInvitationEmailRefusedThenSentAgain(t *testing.T) {
	f := newFixture(t)
	f.h.notes = notify.New(f.db, nil, f.clock.now)
	owner := f.activeUser("owner@example.com")
	f.mail.fail.Store(true)
	wantErr(t, f.do(f.sessionReq(http.MethodPost, "/members/invitations", owner, map[string]string{"email": "m@example.com", "role": "member"})),
		503, "email_unavailable", "email_unavailable")
	f.mail.fail.Store(false)
	list := f.listInvitations(owner)
	if len(list) != 1 {
		t.Fatalf("%d invitations after a refused email", len(list))
	}
	rec := f.do(f.sessionReq(http.MethodPost, "/members/invitations/"+list[0].ID+"/resend", owner, nil))
	if rec.Code != http.StatusCreated {
		t.Fatalf("resend %d %s", rec.Code, rec.Body)
	}
	if rec := f.do(post("/invitations/accept", acceptBody(f.latestInviteToken("m@example.com")))); rec.Code != http.StatusOK {
		t.Fatalf("accept %d %s", rec.Code, rec.Body)
	}
}
