package mailer

import (
	"context"
	"net/url"
	"strings"
	"time"
	"unicode"
)

// Dashboard pages the links open, on APP_ORIGIN. A7b and A7c build these
// pages; they must keep these paths or change them here.
const (
	pathLogin  = "/login"
	pathForgot = "/forgot-password"
	pathReset  = "/reset-password"
	pathKeys   = "/keys"
)

// The account messages of spec/accounts.md, "Emails". Each returns the
// transport's error; a caller must not show it to the person (it can say
// whether an address exists, or name our own configuration).

// VerificationCode sends the 6-digit code that activates a pending user.
func (m *Mailer) VerificationCode(ctx context.Context, to, code string) error {
	return m.send(ctx, to, &email{
		Kind:         "verification-code",
		Subject:      "Your Unify Compute verification code",
		Preheader:    "Enter it within 15 minutes to finish creating your account.",
		Eyebrow:      "VERIFY",
		Headline:     "Confirm your email address.",
		Intro:        "Enter this code on the Unify Compute sign-up page to finish creating your account.",
		Code:         code,
		Safety:       "This code expires in 15 minutes and works once. If you didn't sign up for Unify Compute, ignore this email: an account that is never confirmed is deleted after 7 days.",
		FooterReason: reasonAccount,
	})
}

// SignupAttempt tells the owner of a registered address that someone tried
// to sign up with it. It is what signup sends instead of a code, so the
// sign-up page never reveals whether an address is registered.
func (m *Mailer) SignupAttempt(ctx context.Context, to string) error {
	return m.send(ctx, to, &email{
		Kind:         "signup-attempt",
		Subject:      "Someone tried to sign up with your address",
		Preheader:    "Your existing Unify Compute account was not changed.",
		Eyebrow:      "SIGN-UP ATTEMPT",
		Headline:     "This address already has an account.",
		Intro:        "Someone tried to create a Unify Compute account with this email address. You already have one, so no new account was created.",
		CTA:          &cta{Label: "Log in", URL: m.AppOrigin + pathLogin},
		Safety:       "Forgot password? Reset it at " + m.AppOrigin + pathForgot + ". If this wasn't you, there is nothing to do: your password and API keys are unchanged.",
		FooterReason: reasonSignup,
	})
}

// PasswordReset sends the reset link. The token goes in the fragment, so
// the browser never sends it to a server and it never lands in a log.
func (m *Mailer) PasswordReset(ctx context.Context, to, token string) error {
	return m.send(ctx, to, &email{
		Kind:         "password-reset",
		Subject:      "Reset your Unify Compute password",
		Preheader:    "The link works once and expires in 1 hour.",
		Eyebrow:      "RESET",
		Headline:     "Reset your password.",
		Intro:        "Someone asked to reset the password of the Unify Compute account at this address. Choose a new one with the button below.",
		CTA:          &cta{Label: "Reset password", URL: m.AppOrigin + pathReset + "#token=" + url.QueryEscape(token)},
		Safety:       "This link expires in 1 hour and works once. Resetting your password logs you out everywhere. If you didn't ask for this, ignore this email: your password stays the same.",
		FooterReason: reasonAccount,
	})
}

// PasswordChanged confirms a change or a reset. It has no button: the
// person who did not make the change needs the safety line, not a link.
func (m *Mailer) PasswordChanged(ctx context.Context, to string, at time.Time) error {
	return m.send(ctx, to, &email{
		Kind:         "password-changed",
		Subject:      "Your Unify Compute password was changed",
		Preheader:    "If you made this change, there is nothing else to do.",
		Eyebrow:      "SECURITY",
		Headline:     "Your password was changed.",
		Intro:        "The password of the Unify Compute account at this address was changed.",
		Details:      []detail{{"Changed", utc(at)}},
		Safety:       "If you didn't change it, reset your password now at " + m.AppOrigin + pathForgot + ", revoke any API key you don't recognise, and contact us at " + contactEmail + ".",
		FooterReason: reasonAccount,
	})
}

// Key is the API key a KeyCreated email describes. Never the secret.
type Key struct {
	Name      string // chosen by the user
	Display   string // uck_ and the first 8 characters, as GET /keys shows it
	Scopes    []string
	CreatedAt time.Time
}

// KeyCreated tells the account that a key was created, so a key made by
// someone else with a stolen session does not go unnoticed.
func (m *Mailer) KeyCreated(ctx context.Context, to string, k Key) error {
	return m.send(ctx, to, &email{
		Kind:      "key-created",
		Subject:   "A new Unify Compute API key was created",
		Preheader: "Its display prefix is " + oneLine(k.Display) + ".",
		Eyebrow:   "API KEY",
		Headline:  "A new API key was created.",
		Intro:     "An API key was created for your Unify Compute account. Anyone holding it can use it within its scopes until it is revoked or expires.",
		CTA:       &cta{Label: "Review keys", URL: m.AppOrigin + pathKeys},
		Details: []detail{
			{"Name", oneLine(k.Name)},
			{"Key", oneLine(k.Display)},
			{"Scopes", oneLine(strings.Join(k.Scopes, ", "))},
			{"Created", utc(k.CreatedAt)},
		},
		Safety:       "If you didn't create this key, revoke it on the Keys page and reset your password at " + m.AppOrigin + pathForgot + ".",
		FooterReason: reasonAccount,
	})
}

// ComputeEnabled tells the account an operator turned job submission on
// (ADR-0011 D3: off by default, enabled by hand).
func (m *Mailer) ComputeEnabled(ctx context.Context, to string) error {
	return m.send(ctx, to, &email{
		Kind:         "compute-enabled",
		Subject:      "Your Unify Compute account can now run jobs",
		Preheader:    "Jobs are paid for from your prepaid balance.",
		Eyebrow:      "ACCOUNT",
		Headline:     "Your account can now run jobs.",
		Intro:        "Job submission is now on for your Unify Compute account. Create an API key to submit your first job.",
		CTA:          &cta{Label: "Create an API key", URL: m.AppOrigin + pathKeys},
		Safety:       "Each job holds part of your balance while it runs and is charged for the GPU-seconds it uses. If you didn't expect this, contact us at " + contactEmail + ".",
		FooterReason: reasonAccount,
	})
}

func utc(t time.Time) string { return t.UTC().Format("2006-01-02 15:04 UTC") }

// oneLine flattens a value someone chose (a key name) so that a line break
// in it cannot start a new line of the plain-text part. The HTML part is
// escaped by html/template; this is about the text.
func oneLine(s string) string {
	return strings.Join(strings.FieldsFunc(s, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }), " ")
}
