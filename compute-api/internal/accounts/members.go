package accounts

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/FelixSphere/unify-compute/compute-api/internal/store"
)

// Members and invitations (ADR-0011 D9, spec/accounts.md "Members and
// roles"). The account is the tenant; a user is a login with one role in
// exactly one account. The invitation token lifecycle (issue, peek, consume,
// reissue) follows Unify Data's accounts.py, on a table of its own.

const (
	RoleOwner  = "owner"
	RoleAdmin  = "admin"
	RoleMember = "member"

	InvitationTTL = 7 * 24 * time.Hour
	// InvitationShownFor is how long an invitation that expired unused is
	// still listed, so the page can offer "Send again" for it.
	InvitationShownFor = 30 * 24 * time.Hour

	maxAccountName = 64
)

// The audit actions of member management (spec/accounts.md "audit_events").
const (
	ActionInvited        = "invited"
	ActionInviteAccepted = "invite_accepted"
	ActionInviteRevoked  = "invite_revoked"
	ActionMemberRemoved  = "member_removed"
	ActionRoleChanged    = "role_changed"
	ActionAccountRenamed = "account_renamed"
)

// InvitesPerAccount bounds invitation emails, resends included: each one
// goes to an address the account chose, so without it the form is a way to
// send our mail to anyone.
var InvitesPerAccount = Rule{"invite_account", 20, 24 * time.Hour}

var (
	ErrRoleRequired      = &Error{"role_required", "", "Your role in this account does not allow this. Ask an owner or admin of the account."}
	ErrInviteRole        = &Error{"invalid_role", "role", "Choose the role admin or member."}
	ErrMemberRole        = &Error{"invalid_role", "role", "Choose the role owner, admin or member."}
	ErrAlreadyMember     = &Error{"already_member", "email", "This address is already a member of this account."}
	ErrMemberLimit       = &Error{"member_limit_reached", "", "This account has 20 members and invitations already. Remove someone or withdraw an invitation first, or contact us to raise the limit."}
	ErrLastOwner         = &Error{"last_owner", "", "An account needs at least one owner. Make another member an owner first."}
	ErrEmailInUse        = &Error{"email_in_use", "", "This address already has a Unify Compute login, and a login belongs to one account. Log in with it instead, or ask to be invited at another address."}
	ErrInvalidAcctName   = &Error{"invalid_name", "name", "Name the account with 1 to 64 characters, without line breaks, control characters or angle brackets."}
	ErrSuspendedInvite   = &Error{"account_suspended", "", "This account is suspended and cannot invite anyone. Contact us."}
	ErrInvitationInvalid = errors.New("accounts: invitation expired, withdrawn, used or unknown")
)

// ValidRole reports whether role is one of the three.
func ValidRole(role string) bool { return role == RoleOwner || role == RoleAdmin || role == RoleMember }

// CanManage reports whether role may invite, withdraw invitations, remove
// members and rename the account.
func CanManage(role string) bool { return role == RoleOwner || role == RoleAdmin }

// CanInviteAs reports whether actor may send (or resend, or withdraw) an
// invitation carrying role: an admin manages members only.
func CanInviteAs(actor, role string) bool {
	return actor == RoleOwner || actor == RoleAdmin && role == RoleMember
}

// CanRemove: an owner removes anyone, an admin removes members, and anyone
// removes themself.
func CanRemove(actor User, target User) bool {
	return actor.ID == target.ID || actor.Role == RoleOwner || actor.Role == RoleAdmin && target.Role == RoleMember
}

// ValidateAccountName trims and checks a new account name the way key names
// are checked, at 1-64 characters.
func ValidateAccountName(name string) (string, error) {
	name = strings.TrimSpace(name)
	n := utf8.RuneCountInString(name)
	if n == 0 || n > maxAccountName || !utf8.ValidString(name) {
		return "", ErrInvalidAcctName
	}
	if strings.IndexFunc(name, func(r rune) bool {
		return unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || r == '<' || r == '>' || r == utf8.RuneError
	}) >= 0 {
		return "", ErrInvalidAcctName
	}
	return name, nil
}

// RenameAccount sets the account's name.
func (s *Service) RenameAccount(ctx context.Context, accountID int64, name string) (Account, error) {
	name, err := ValidateAccountName(name)
	if err != nil {
		return Account{}, err
	}
	return scanAccount(s.db.QueryRow(ctx, `UPDATE accounts AS a SET name = $2 WHERE a.id = $1 RETURNING `+accountColumns, accountID, name))
}

// ListMembers returns the account's users that are not closed, oldest first.
func (s *Service) ListMembers(ctx context.Context, accountID int64) ([]User, error) {
	rows, err := s.db.Query(ctx, `SELECT `+userColumns+` FROM users u
		WHERE u.account_id = $1 AND u.status <> 'closed' ORDER BY u.created_at, u.id`, accountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []User{}
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// Member returns one user of the account; a closed user, or another
// account's, is ErrNotFound.
func (s *Service) Member(ctx context.Context, accountID, userID int64) (User, error) {
	return scanUser(s.db.QueryRow(ctx, `SELECT `+userColumns+` FROM users u
		WHERE u.id = $2 AND u.account_id = $1 AND u.status <> 'closed'`, accountID, userID))
}

// lockAccount serialises member changes of one account, so two requests
// cannot both take the last free slot or both demote one of two owners.
func lockAccount(ctx context.Context, tx pgx.Tx, accountID int64) (Account, error) {
	return scanAccount(tx.QueryRow(ctx, `SELECT `+accountColumns+` FROM accounts a WHERE a.id = $1 FOR UPDATE`, accountID))
}

// otherOwners counts the active owners of the account other than userID.
func otherOwners(ctx context.Context, tx pgx.Tx, accountID, userID int64) (int, error) {
	var n int
	err := tx.QueryRow(ctx, `SELECT count(*) FROM users
		WHERE account_id = $1 AND role = 'owner' AND status = 'active' AND id <> $2`, accountID, userID).Scan(&n)
	return n, err
}

// ChangeRole gives a member another role. Taking owner from the last active
// owner is ErrLastOwner. It returns the user as it now is and its old role.
func (s *Service) ChangeRole(ctx context.Context, accountID, userID int64, role string) (User, string, error) {
	if !ValidRole(role) {
		return User{}, "", ErrMemberRole
	}
	var u User
	var old string
	err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		if _, err := lockAccount(ctx, tx, accountID); err != nil {
			return err
		}
		var err error
		u, err = scanUser(tx.QueryRow(ctx, `SELECT `+userColumns+` FROM users u
			WHERE u.id = $2 AND u.account_id = $1 AND u.status <> 'closed' FOR UPDATE`, accountID, userID))
		if err != nil {
			return err
		}
		old = u.Role
		if old == role {
			return nil
		}
		if old == RoleOwner {
			n, err := otherOwners(ctx, tx, accountID, userID)
			if err != nil {
				return err
			}
			if n == 0 {
				return ErrLastOwner
			}
		}
		if _, err := tx.Exec(ctx, `UPDATE users SET role = $2 WHERE id = $1`, userID, role); err != nil {
			return err
		}
		u.Role = role
		return nil
	})
	if err != nil {
		return User{}, "", err
	}
	return u, old, nil
}

// RemoveMember closes a user of the account: its sessions end and its
// address is tombstoned (closed+<id>@invalid), so it can be invited again
// or sign up afresh. Keys it created stay; they belong to the account.
// Removing the last active owner is ErrLastOwner. It returns the user as it
// was, with its address, for the email.
func (s *Service) RemoveMember(ctx context.Context, accountID, userID int64) (User, error) {
	var u User
	err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		if _, err := lockAccount(ctx, tx, accountID); err != nil {
			return err
		}
		var err error
		u, err = scanUser(tx.QueryRow(ctx, `SELECT `+userColumns+` FROM users u
			WHERE u.id = $2 AND u.account_id = $1 AND u.status <> 'closed' FOR UPDATE`, accountID, userID))
		if err != nil {
			return err
		}
		if u.Role == RoleOwner {
			n, err := otherOwners(ctx, tx, accountID, userID)
			if err != nil {
				return err
			}
			if n == 0 {
				return ErrLastOwner
			}
		}
		if _, err := tx.Exec(ctx, `UPDATE users SET status = 'closed', email = 'closed+' || id || '@invalid' WHERE id = $1`, userID); err != nil {
			return err
		}
		return deleteSessions(ctx, tx, userID, "")
	})
	return u, err
}

// Invitation is one row of invitations, with the inviter's address when the
// inviter still exists.
type Invitation struct {
	ID             string
	AccountID      int64
	Email          string
	Role           string
	InvitedBy      *int64
	InvitedByEmail string
	CreatedAt      time.Time
	ExpiresAt      time.Time
}

// Expired reports whether the invitation can no longer be accepted.
func (i Invitation) Expired(now time.Time) bool { return !now.Before(i.ExpiresAt) }

const invitationColumns = `i.id, i.account_id, i.email, i.role, i.invited_by, coalesce(CASE WHEN iu.status <> 'closed' THEN iu.email END, ''),
	i.created_at, i.expires_at`

// open: not accepted, revoked or superseded. Expiry is checked apart.
const invitationOpen = `i.accepted_at IS NULL AND i.revoked_at IS NULL AND i.superseded_at IS NULL`

func scanInvitation(row pgx.Row) (Invitation, error) {
	var i Invitation
	err := row.Scan(&i.ID, &i.AccountID, &i.Email, &i.Role, &i.InvitedBy, &i.InvitedByEmail, &i.CreatedAt, &i.ExpiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Invitation{}, ErrNotFound
	}
	return i, err
}

// Invite issues an invitation for email to join the account as role (admin
// or member), superseding any open one for the same address. An address
// that is already a user of this account is ErrAlreadyMember; the members
// and live invitations of the account, the new one included, may not exceed
// its member_limit (ErrMemberLimit). A suspended or closed account cannot
// invite. Returns the invitation and the token for the email link.
func (s *Service) Invite(ctx context.Context, accountID, inviterID int64, email, role string) (Invitation, string, error) {
	email = NormalizeEmail(email)
	if err := ValidateEmail(email); err != nil {
		return Invitation{}, "", err
	}
	if role != RoleAdmin && role != RoleMember {
		return Invitation{}, "", ErrInviteRole
	}
	var inv Invitation
	var token string
	err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		var err error
		inv, token, err = s.issueInvitation(ctx, tx, accountID, inviterID, email, role)
		return err
	})
	if err != nil {
		return Invitation{}, "", err
	}
	return inv, token, nil
}

// ReissueInvitation is "Send again": the open invitation id, live or
// expired, is superseded and a new one with a fresh token and lifetime goes
// to the same address for the same role. Unknown, accepted, withdrawn or
// superseded is ErrNotFound.
func (s *Service) ReissueInvitation(ctx context.Context, accountID, inviterID int64, id string) (Invitation, string, error) {
	var inv Invitation
	var token string
	err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		old, err := scanInvitation(tx.QueryRow(ctx, `SELECT `+invitationColumns+` FROM invitations i
			LEFT JOIN users iu ON iu.id = i.invited_by
			WHERE i.id = $1 AND i.account_id = $2 AND `+invitationOpen, id, accountID))
		if err != nil {
			return err
		}
		inv, token, err = s.issueInvitation(ctx, tx, accountID, inviterID, old.Email, old.Role)
		return err
	})
	if err != nil {
		return Invitation{}, "", err
	}
	return inv, token, nil
}

func (s *Service) issueInvitation(ctx context.Context, tx pgx.Tx, accountID, inviterID int64, email, role string) (Invitation, string, error) {
	a, err := lockAccount(ctx, tx, accountID)
	if err != nil {
		return Invitation{}, "", err
	}
	if a.Status != AccountActive {
		return Invitation{}, "", ErrSuspendedInvite
	}
	now := s.now()
	var member bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM users WHERE account_id = $1 AND email = $2 AND status <> 'closed')`,
		accountID, email).Scan(&member); err != nil {
		return Invitation{}, "", err
	}
	if member {
		return Invitation{}, "", ErrAlreadyMember
	}
	// The invitation this one supersedes does not count against the limit.
	var used int
	if err := tx.QueryRow(ctx, `SELECT
		(SELECT count(*) FROM users WHERE account_id = $1 AND status <> 'closed') +
		(SELECT count(*) FROM invitations i WHERE i.account_id = $1 AND `+invitationOpen+` AND i.expires_at > $2 AND i.email <> $3)`,
		accountID, now, email).Scan(&used); err != nil {
		return Invitation{}, "", err
	}
	if used >= a.MemberLimit {
		e := *ErrMemberLimit
		e.Message = "This account has " + strconv.Itoa(a.MemberLimit) + " members and invitations already. Remove someone or withdraw an invitation first, or contact us to raise the limit."
		return Invitation{}, "", &e
	}
	if _, err := tx.Exec(ctx, `UPDATE invitations i SET superseded_at = $3
		WHERE i.account_id = $1 AND i.email = $2 AND `+invitationOpen, accountID, email, now); err != nil {
		return Invitation{}, "", err
	}
	token, err := randomToken()
	if err != nil {
		return Invitation{}, "", err
	}
	id := store.NewID("inv_", now)
	inv, err := scanInvitation(tx.QueryRow(ctx, `WITH i AS (
			INSERT INTO invitations (id, account_id, email, role, token_digest, invited_by, created_at, expires_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8) RETURNING *)
		SELECT `+invitationColumns+` FROM i LEFT JOIN users iu ON iu.id = i.invited_by`,
		id, accountID, email, role, digest(token), inviterID, now, now.Add(InvitationTTL)))
	if err != nil {
		return Invitation{}, "", err
	}
	return inv, token, nil
}

// ListInvitations returns the account's open invitations, newest first:
// the live ones, and those that expired within InvitationShownFor.
func (s *Service) ListInvitations(ctx context.Context, accountID int64) ([]Invitation, error) {
	rows, err := s.db.Query(ctx, `SELECT `+invitationColumns+` FROM invitations i
		LEFT JOIN users iu ON iu.id = i.invited_by
		WHERE i.account_id = $1 AND `+invitationOpen+` AND i.expires_at > $2
		ORDER BY i.created_at DESC, i.id DESC`, accountID, s.now().Add(-InvitationShownFor))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Invitation{}
	for rows.Next() {
		i, err := scanInvitation(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, i)
	}
	return out, rows.Err()
}

// InvitationByID returns one open invitation of the account, live or
// expired; anything else is ErrNotFound.
func (s *Service) InvitationByID(ctx context.Context, accountID int64, id string) (Invitation, error) {
	return scanInvitation(s.db.QueryRow(ctx, `SELECT `+invitationColumns+` FROM invitations i
		LEFT JOIN users iu ON iu.id = i.invited_by
		WHERE i.id = $1 AND i.account_id = $2 AND `+invitationOpen, id, accountID))
}

// RevokeInvitation withdraws an open invitation, live or expired; its link
// stops working at once. Anything else is ErrNotFound.
func (s *Service) RevokeInvitation(ctx context.Context, accountID int64, id string) (Invitation, error) {
	inv, err := s.InvitationByID(ctx, accountID, id)
	if err != nil {
		return Invitation{}, err
	}
	tag, err := s.db.Exec(ctx, `UPDATE invitations i SET revoked_at = $3
		WHERE i.id = $1 AND i.account_id = $2 AND `+invitationOpen, id, accountID, s.now())
	if err != nil {
		return Invitation{}, err
	}
	if tag.RowsAffected() == 0 {
		return Invitation{}, ErrNotFound
	}
	return inv, nil
}

// PeekedInvitation is what the accept page shows before anything is used.
type PeekedInvitation struct {
	Invitation
	AccountName string
}

// PeekInvitation resolves a live token without using it. Expired,
// withdrawn, superseded, accepted, unknown, or the account closed:
// ErrInvitationInvalid, one answer for all.
func (s *Service) PeekInvitation(ctx context.Context, token string) (PeekedInvitation, error) {
	var p PeekedInvitation
	err := s.db.QueryRow(ctx, `SELECT `+invitationColumns+`, a.name FROM invitations i
		JOIN accounts a ON a.id = i.account_id
		LEFT JOIN users iu ON iu.id = i.invited_by
		WHERE i.token_digest = $1 AND `+invitationOpen+` AND i.expires_at > $2 AND a.status <> 'closed'`,
		digest(token), s.now()).Scan(&p.ID, &p.AccountID, &p.Email, &p.Role, &p.InvitedBy, &p.InvitedByEmail,
		&p.CreatedAt, &p.ExpiresAt, &p.AccountName)
	if errors.Is(err, pgx.ErrNoRows) {
		return PeekedInvitation{}, ErrInvitationInvalid
	}
	return p, err
}

// AcceptInvitation consumes a live token and creates the invited user:
// active, verified (the link proves the address), with the invitation's
// role and passwordHash, which the caller validated against the invited
// address and hashed. An address that already belongs to any user is
// ErrEmailInUse, and the invitation stays open.
func (s *Service) AcceptInvitation(ctx context.Context, token, passwordHash string) (User, PeekedInvitation, error) {
	now := s.now()
	var u User
	var p PeekedInvitation
	err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `SELECT `+invitationColumns+`, a.name FROM invitations i
			JOIN accounts a ON a.id = i.account_id
			LEFT JOIN users iu ON iu.id = i.invited_by
			WHERE i.token_digest = $1 AND `+invitationOpen+` AND i.expires_at > $2 AND a.status <> 'closed'
			FOR UPDATE OF i`, digest(token), now).Scan(&p.ID, &p.AccountID, &p.Email, &p.Role, &p.InvitedBy,
			&p.InvitedByEmail, &p.CreatedAt, &p.ExpiresAt, &p.AccountName)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrInvitationInvalid
		}
		if err != nil {
			return err
		}
		u, err = scanUser(tx.QueryRow(ctx, `INSERT INTO users AS u
			(account_id, role, email, password_hash, status, email_verified_at, password_changed_at, last_login_at, created_at)
			VALUES ($1, $2, $3, $4, 'active', $5, $5, $5, $5) RETURNING `+userColumns,
			p.AccountID, p.Role, p.Email, passwordHash, now))
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return ErrEmailInUse
		}
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `UPDATE invitations SET accepted_at = $2 WHERE id = $1`, p.ID, now)
		return err
	})
	if err != nil {
		return User{}, PeekedInvitation{}, err
	}
	return u, p, nil
}

// KeyByID returns one of the account's keys, revoked or not; another
// account's is ErrNotFound. Revocation checks who created it.
func (s *Service) KeyByID(ctx context.Context, accountID, keyID int64) (APIKey, error) {
	return scanKey(s.db.QueryRow(ctx, `SELECT `+keyColumns+` FROM api_keys k WHERE k.id = $1 AND k.account_id = $2`, keyID, accountID))
}
