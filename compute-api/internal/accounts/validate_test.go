package accounts

import (
	"errors"
	"strings"
	"testing"
)

func TestNormalizeEmail(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]string{
		"  Alice@Example.COM \n":      "alice@example.com",
		"jos\u0065\u0301@example.com": "jos\u00e9@example.com", // decomposed é becomes the composed one
		"JOS\u00c9@EXAMPLE.COM":       "jos\u00e9@example.com",
	} {
		if got := NormalizeEmail(in); got != want {
			t.Errorf("NormalizeEmail(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestValidateEmail(t *testing.T) {
	t.Parallel()
	good := []string{"a@b.co", "first.last+tag@sub.example.com", "josé@example.com"}
	bad := []string{"", "plain", "@example.com", "a@", "a@localhost", "a@@b.com", "a@b@c.com",
		"a@.example.com", "a@example.com.", "a@example..com", "a b@example.com", "a@exa\tmple.com",
		strings.Repeat("a", 250) + "@b.co"}
	for _, e := range good {
		if err := ValidateEmail(e); err != nil {
			t.Errorf("ValidateEmail(%q) = %v, want nil", e, err)
		}
	}
	for _, e := range bad {
		if err := ValidateEmail(e); err != ErrInvalidEmail {
			t.Errorf("ValidateEmail(%q) = %v, want ErrInvalidEmail", e, err)
		}
	}
}

func TestValidatePassword(t *testing.T) {
	t.Parallel()
	const email = "alice.smith@example.com"
	cases := []struct {
		pw   string
		want error
	}{
		{"correct horse", nil},
		{"short pw 11", ErrWeakPassword},
		{strings.Repeat("abcde", 25) + "xyz", nil},              // 128 characters
		{strings.Repeat("abcde", 25) + "wxyz", ErrWeakPassword}, // 129
		{"aaaabbbbccccdddd", ErrWeakPassword},                   // 4 distinct
		{"aaaabbbbccccdddde", nil},                              // 5 distinct
		{email, ErrWeakPassword},
		{"  Alice.Smith@Example.com ", ErrWeakPassword},
		{"alice.smith", ErrWeakPassword},  // 11 characters anyway
		{"ALICE.SMITH@", nil},             // neither the address nor the local part
		{"密码密码密码密码密码密码", ErrWeakPassword}, // 12 characters, 2 distinct
		{"一二三四五六七八九十百千", nil},             // 12 characters counted as characters, not bytes
	}
	for _, c := range cases {
		if got := ValidatePassword(email, c.pw); got != c.want {
			t.Errorf("ValidatePassword(%q) = %v, want %v", c.pw, got, c.want)
		}
	}
	// The local part on its own, long enough to pass every other rule.
	if err := ValidatePassword("correcthorse@example.com", "CorrectHorse"); err != ErrWeakPassword {
		t.Errorf("local part as password: got %v", err)
	}
}

func TestValidateNewPasswordChecksTheConfirmationFirst(t *testing.T) {
	t.Parallel()
	if err := ValidateNewPassword("a@b.co", "short", "other"); err != ErrPasswordMismatch {
		t.Fatalf("got %v, want ErrPasswordMismatch", err)
	}
	if err := ValidateNewPassword("a@b.co", "correct horse", "correct horse"); err != nil {
		t.Fatalf("got %v", err)
	}
	var e *Error
	if !errors.As(ErrPasswordMismatch, &e) || e.Code != "password_mismatch" || e.Param != "password_confirm" {
		t.Fatalf("error carries the spec's code and param: %+v", e)
	}
}
