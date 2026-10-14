package mailer

import (
	"context"
	"net/url"
	"time"
)

// The member messages of spec/accounts.md "Emails": invitation, invitation
// accepted, removed from account. Kept apart from messages.go so the
// account messages and these can change without touching each other.

// Message kinds, also the notify.Key Message of each.
const (
	KindInvitation         = "invitation"
	KindInvitationAccepted = "invitation-accepted"
	KindMemberRemoved      = "member-removed"
)

const (
	pathMembers = "/members"
	pathAccept  = "/accept-invitation"

	reasonInvited = "You're receiving this because a member of a Unify Compute account invited this address."
)

// Invite is what an Invitation email says. Token is the invitation's
// secret; it goes in the link's fragment only.
type Invite struct {
	AccountName  string
	InviterEmail string // empty if the inviter has since left
	Role         string
	Token        string
	ExpiresAt    time.Time
}

// roleLabel is the role as the dashboard names it.
func roleLabel(role string) string {
	switch role {
	case "owner":
		return "Owner"
	case "admin":
		return "Admin"
	default:
		return "Member"
	}
}

// Invitation asks an address to join an account. The link proves the
// address, so accepting needs no verification code.
func (m *Mailer) Invitation(ctx context.Context, to string, in Invite) error {
	account := oneLine(in.AccountName)
	inviter := oneLine(in.InviterEmail)
	subject := "You were invited to " + account + " on Unify Compute"
	intro := "You were invited to join " + account + " on Unify Compute with the " + roleLabel(in.Role) + " role."
	if inviter != "" {
		subject = inviter + " invited you to " + account + " on Unify Compute"
		intro = inviter + " invited you to join " + account + " on Unify Compute with the " + roleLabel(in.Role) + " role."
	}
	details := []detail{{"Account", account}}
	if inviter != "" {
		details = append(details, detail{"Invited by", inviter})
	}
	details = append(details, detail{"Role", roleLabel(in.Role)}, detail{"Expires", utc(in.ExpiresAt)})
	return m.send(ctx, to, &email{
		Kind:         KindInvitation,
		Subject:      subject,
		Preheader:    "Choose a password to accept. The link works once and expires in 7 days.",
		Eyebrow:      "INVITATION",
		Headline:     "Join " + account + " on Unify Compute.",
		Intro:        intro + " Members share the account's balance, API keys and jobs.",
		CTA:          &cta{Label: "Accept invitation", URL: m.AppOrigin + pathAccept + "#token=" + url.QueryEscape(in.Token)},
		Details:      details,
		Safety:       "This link expires in 7 days and works once. If you weren't expecting this, ignore this email: nothing is created until you accept.",
		FooterReason: reasonInvited,
	})
}

// Joined is what an InvitationAccepted email says.
type Joined struct {
	Email       string // the new member
	AccountName string
	Role        string
	At          time.Time
}

// InvitationAccepted tells the inviter that the invitation was accepted.
func (m *Mailer) InvitationAccepted(ctx context.Context, to string, j Joined) error {
	who, account := oneLine(j.Email), oneLine(j.AccountName)
	return m.send(ctx, to, &email{
		Kind:      KindInvitationAccepted,
		Subject:   who + " joined " + account + " on Unify Compute",
		Preheader: "They can now use the account's balance and create API keys.",
		Eyebrow:   "MEMBERS",
		Headline:  who + " joined " + account + ".",
		Intro:     "The invitation you sent was accepted. The new member can see the account's balance, jobs and usage, and create API keys that spend its balance.",
		CTA:       &cta{Label: "View members", URL: m.AppOrigin + pathMembers},
		Details: []detail{
			{"Member", who},
			{"Role", roleLabel(j.Role)},
			{"Joined", utc(j.At)},
		},
		Safety:       "If you don't recognise this person, remove them on the Members page and revoke any API key they created.",
		FooterReason: reasonAccount,
	})
}

// Removal is what a RemovedFromAccount email says.
type Removal struct {
	AccountName string
	ByEmail     string // empty when the person removed themself
	At          time.Time
}

// RemovedFromAccount tells a removed address that its login is closed.
func (m *Mailer) RemovedFromAccount(ctx context.Context, to string, r Removal) error {
	account, by := oneLine(r.AccountName), oneLine(r.ByEmail)
	intro := "You left " + account + " on Unify Compute. Your login to it is closed and its sessions have ended."
	safety := "If you didn't do this, contact an owner of " + account + ", or us at " + contactEmail + "."
	details := []detail{{"Account", account}, {"Removed", utc(r.At)}}
	if by != "" {
		intro = by + " removed you from " + account + " on Unify Compute. Your login to it is closed and its sessions have ended."
		safety = "If you think this is a mistake, contact " + by + " or an owner of " + account + ". API keys you created belong to the account and keep working until it revokes them."
		details = []detail{{"Account", account}, {"Removed by", by}, {"Removed", utc(r.At)}}
	}
	return m.send(ctx, to, &email{
		Kind:         KindMemberRemoved,
		Subject:      "You were removed from " + account + " on Unify Compute",
		Preheader:    "Your login to this account is closed.",
		Eyebrow:      "MEMBERS",
		Headline:     "You were removed from " + account + ".",
		Intro:        intro,
		Details:      details,
		Safety:       safety,
		FooterReason: reasonAccount,
	})
}
