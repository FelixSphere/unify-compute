// Package mailer renders and sends Unify Compute's transactional email
// (ADR-0011, spec/accounts.md "Emails"): multipart plain text plus HTML in
// the shared UnifyAI layout (docs/spec/email/base.html), sent as one raw
// MIME message through SES v2 with the instance role.
//
// Without SES_SENDER the messages are written to an outbox directory, and
// only when ENVIRONMENT=development. Unify Data falls back to its outbox
// silently in any environment, which in production means signups that never
// receive their code; here a deployment with local accounts and no sender
// refuses to start instead.
package mailer

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net/mail"
	"net/textproto"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/FelixSphere/unify-compute/compute-api/internal/awsv4"
)

// Transport delivers one rendered message.
type Transport interface {
	Deliver(ctx context.Context, kind, to string, raw []byte) error
	// Check proves at boot that Deliver can work, as far as that can be
	// known without sending mail.
	Check(ctx context.Context) error
}

// Mailer renders the messages in messages.go and hands them to Transport.
type Mailer struct {
	From      mail.Address
	AppOrigin string // links in the messages point here, e.g. https://app.compute.unifyai.us
	Transport Transport
	Now       func() time.Time
}

// Settings is the mailer's part of the environment.
type Settings struct {
	Sender           string // SES_SENDER
	ConfigurationSet string // SES_CONFIGURATION_SET
	AppOrigin        string // APP_ORIGIN
	OutboxDir        string // MAIL_OUTBOX_DIR, development only
	AccountsBackend  string // ACCOUNTS_BACKEND: "console" (default) or "local"
	Environment      string // ENVIRONMENT
}

func SettingsFromEnv(getenv func(string) string) Settings {
	return Settings{
		Sender:           strings.TrimSpace(getenv("SES_SENDER")),
		ConfigurationSet: strings.TrimSpace(getenv("SES_CONFIGURATION_SET")),
		AppOrigin:        strings.TrimRight(strings.TrimSpace(getenv("APP_ORIGIN")), "/"),
		OutboxDir:        getenv("MAIL_OUTBOX_DIR"),
		AccountsBackend:  getenv("ACCOUNTS_BACKEND"),
		Environment:      getenv("ENVIRONMENT"),
	}
}

// Required reports whether this deployment must be able to send: with
// local accounts, signup and reset cannot work without mail.
func (s Settings) Required() bool { return s.AccountsBackend == "local" }

// New builds the mailer the settings describe. It returns nil and no error
// when nothing needs mail (console accounts, no sender): until the cutover
// the console sends account mail and compute sends none.
func New(s Settings, client *awsv4.Client, creds awsv4.CredentialSource) (*Mailer, error) {
	dev := s.Environment == "development"
	var t Transport
	var from mail.Address
	switch {
	case s.Sender != "":
		a, err := mail.ParseAddress(s.Sender)
		if err != nil {
			return nil, fmt.Errorf("SES_SENDER %q: %w (quote the whole value in .env: 'Unify Compute <no-reply@compute.unifyai.us>')", s.Sender, err)
		}
		if a.Name == "" {
			return nil, fmt.Errorf("SES_SENDER %q has no display name; the kit requires the product name, e.g. 'Unify Compute <no-reply@compute.unifyai.us>'", s.Sender)
		}
		from = *a
		t = &SES{Client: client, Creds: creds, From: a.Address, ConfigurationSet: s.ConfigurationSet}
	case dev:
		dir := s.OutboxDir
		if dir == "" {
			dir = "outbox"
		}
		from = mail.Address{Name: ProductName, Address: "no-reply@compute.unifyai.us"}
		t = &Outbox{Dir: dir}
	case s.Required():
		return nil, errors.New("ACCOUNTS_BACKEND=local needs SES_SENDER: without it no verification code or reset link is ever delivered. Set SES_SENDER, or ENVIRONMENT=development to write mail to an outbox directory instead")
	default:
		return nil, nil
	}
	u, err := url.Parse(s.AppOrigin)
	if err != nil || u.Host == "" || u.Path != "" || u.RawQuery != "" || u.Fragment != "" ||
		!(u.Scheme == "https" || dev && u.Scheme == "http") {
		return nil, fmt.Errorf("APP_ORIGIN %q: the mailer needs the dashboard's origin for its links, e.g. https://app.compute.unifyai.us (http only with ENVIRONMENT=development)", s.AppOrigin)
	}
	return &Mailer{From: from, AppOrigin: s.AppOrigin, Transport: t, Now: time.Now}, nil
}

func (m *Mailer) send(ctx context.Context, to string, e *email) error {
	raw, err := m.compose(to, e)
	if err != nil {
		return err
	}
	return m.Transport.Deliver(ctx, e.Kind, to, raw)
}

// compose renders e for to as a multipart/alternative message. to must be a
// bare address; anything that would need a header to carry more (a display
// name, a second address, a line break) is refused rather than encoded.
func (m *Mailer) compose(to string, e *email) ([]byte, error) {
	a, err := mail.ParseAddress(to)
	if err != nil || a.Name != "" || a.Address != to {
		return nil, fmt.Errorf("mailer: recipient is not a bare address")
	}
	htmlPart, err := e.html()
	if err != nil {
		return nil, fmt.Errorf("mailer: render %s: %w", e.Kind, err)
	}
	now := time.Now
	if m.Now != nil {
		now = m.Now
	}
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	for _, part := range []struct{ ctype, content string }{
		{"text/plain; charset=UTF-8", e.text()},
		{"text/html; charset=UTF-8", htmlPart},
	} {
		w, err := mw.CreatePart(textproto.MIMEHeader{
			"Content-Type":              {part.ctype},
			"Content-Transfer-Encoding": {"quoted-printable"},
		})
		if err != nil {
			return nil, err
		}
		qp := quotedprintable.NewWriter(w)
		if _, err := qp.Write([]byte(strings.ReplaceAll(part.content, "\n", "\r\n"))); err != nil {
			return nil, err
		}
		if err := qp.Close(); err != nil {
			return nil, err
		}
	}
	if err := mw.Close(); err != nil {
		return nil, err
	}

	var msg bytes.Buffer
	h := func(k, v string) { msg.WriteString(k + ": " + v + "\r\n") }
	h("From", m.From.String())
	h("To", a.String())
	// QEncoding leaves plain ASCII alone and encodes anything else, a
	// line break included, so no subject can add a header.
	h("Subject", mime.QEncoding.Encode("UTF-8", e.Subject))
	h("Date", now().UTC().Format(time.RFC1123Z))
	h("Message-ID", "<"+randomID()+"@"+domainOf(m.From.Address)+">")
	h("MIME-Version", "1.0")
	h("Auto-Submitted", "auto-generated")
	h("Content-Type", `multipart/alternative; boundary="`+mw.Boundary()+`"`)
	msg.WriteString("\r\n")
	msg.Write(body.Bytes())
	return msg.Bytes(), nil
}

func randomID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func domainOf(addr string) string {
	_, d, _ := strings.Cut(addr, "@")
	return d
}

// SES sends through SES v2 SendEmail with the instance role (the role
// grants ses:SendEmail on the identity and the configuration set,
// deploy/aws/modules/compute-stack).
type SES struct {
	Client           *awsv4.Client
	Creds            awsv4.CredentialSource
	From             string
	ConfigurationSet string
}

func (s *SES) Deliver(ctx context.Context, kind, to string, raw []byte) error {
	c, err := s.Creds.Credentials(ctx)
	if err != nil {
		return fmt.Errorf("mailer: aws credentials: %w", err)
	}
	if _, err := s.Client.SendRawEmail(ctx, c, s.From, []string{to}, raw, s.ConfigurationSet); err != nil {
		return fmt.Errorf("mailer: ses %s: %w", kind, err)
	}
	return nil
}

// Check fetches the credentials. It does not call SES: there is no SES call
// that proves sending works without sending, and the identity and its
// permissions are proved by A8's send to the SES mailbox simulator.
func (s *SES) Check(ctx context.Context) error {
	_, err := s.Creds.Credentials(ctx)
	return err
}

// Outbox writes each message to Dir as an .eml file, for development only.
type Outbox struct{ Dir string }

func (o *Outbox) Deliver(_ context.Context, kind, _ string, raw []byte) error {
	if err := os.MkdirAll(o.Dir, 0o700); err != nil {
		return err
	}
	name := fmt.Sprintf("%d-%s-%s.eml", time.Now().UnixMilli(), kind, randomID()[:8])
	return os.WriteFile(filepath.Join(o.Dir, name), raw, 0o600)
}

func (o *Outbox) Check(context.Context) error { return os.MkdirAll(o.Dir, 0o700) }
