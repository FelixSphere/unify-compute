package accounts

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"
)

const (
	MinPasswordLength = 12
	MaxPasswordLength = 128
	minDistinctChars  = 5
	maxEmailLength    = 254
)

// NormalizeEmail is the form an address is stored and looked up in: NFC,
// trimmed, lower-cased. Two spellings that differ only in Unicode
// composition or case are one account.
func NormalizeEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(norm.NFC.String(email)))
}

// ValidateEmail checks an already normalised address: at most 254 bytes, one
// "@", a non-empty local part, and a domain with a dot that neither starts
// nor ends it. Deliverability is the verification code's job, not this one's.
func ValidateEmail(email string) error {
	if email == "" || len(email) > maxEmailLength || !utf8.ValidString(email) {
		return ErrInvalidEmail
	}
	if strings.IndexFunc(email, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) >= 0 {
		return ErrInvalidEmail
	}
	local, domain, ok := strings.Cut(email, "@")
	if !ok || local == "" || strings.Contains(domain, "@") {
		return ErrInvalidEmail
	}
	if !strings.Contains(domain, ".") || strings.HasPrefix(domain, ".") || strings.HasSuffix(domain, ".") || strings.Contains(domain, "..") {
		return ErrInvalidEmail
	}
	return nil
}

// ValidatePassword applies the rules copied from Unify Data: 12 to 128
// characters, not the email or its local part, at least 5 distinct
// characters. email is the normalised address. Lengths count characters,
// not bytes, so a passphrase in any script gets the same allowance.
func ValidatePassword(email, password string) error {
	n := utf8.RuneCountInString(password)
	if n < MinPasswordLength || n > MaxPasswordLength {
		return ErrWeakPassword
	}
	folded := NormalizeEmail(password)
	if local, _, _ := strings.Cut(email, "@"); folded == email || folded == local {
		return ErrWeakPassword
	}
	distinct := map[rune]struct{}{}
	for _, r := range password {
		distinct[r] = struct{}{}
	}
	if len(distinct) < minDistinctChars {
		return ErrWeakPassword
	}
	return nil
}

// ValidateNewPassword is what signup, reset and change run on the submitted
// pair. The page checks the confirmation first; the server checks again.
func ValidateNewPassword(email, password, confirm string) error {
	if password != confirm {
		return ErrPasswordMismatch
	}
	return ValidatePassword(email, password)
}
