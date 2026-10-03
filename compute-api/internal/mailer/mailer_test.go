package mailer

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net/mail"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/FelixSphere/unify-compute/compute-api/internal/awsv4"
	"github.com/FelixSphere/unify-compute/compute-api/internal/awsv4/awsv4test"
)

var update = flag.Bool("update", false, "rewrite testdata/*.golden from the current output")

var at = time.Date(2026, 10, 3, 14, 5, 0, 0, time.UTC)

const origin = "https://app.compute.unifyai.us"

// capture is a Transport that keeps what it is given.
type capture struct {
	kind, to string
	raw      []byte
}

func (c *capture) Deliver(_ context.Context, kind, to string, raw []byte) error {
	c.kind, c.to, c.raw = kind, to, raw
	return nil
}
func (c *capture) Check(context.Context) error { return nil }

func testMailer(t Transport) *Mailer {
	return &Mailer{
		From:      mail.Address{Name: "Unify Compute", Address: "no-reply@compute.unifyai.us"},
		AppOrigin: origin, Transport: t, Now: func() time.Time { return at },
	}
}

// every message A2 owns, with the arguments its golden files were made from.
var messages = []struct {
	kind string
	send func(m *Mailer, to string) error
}{
	{"verification-code", func(m *Mailer, to string) error { return m.VerificationCode(context.Background(), to, "042917") }},
	{"signup-attempt", func(m *Mailer, to string) error { return m.SignupAttempt(context.Background(), to) }},
	{"password-reset", func(m *Mailer, to string) error {
		return m.PasswordReset(context.Background(), to, "q7Vb2xKp9LmN4rT8wYz1aBcDeFgHiJkL0MnOpQrStUv")
	}},
	{"password-changed", func(m *Mailer, to string) error { return m.PasswordChanged(context.Background(), to, at) }},
	{"key-created", func(m *Mailer, to string) error {
		return m.KeyCreated(context.Background(), to, Key{Name: "training laptop", Display: "uck_AbCd1234", Scopes: []string{"jobs", "read"}, CreatedAt: at})
	}},
	{"compute-enabled", func(m *Mailer, to string) error { return m.ComputeEnabled(context.Background(), to) }},
}

type parsed struct {
	header      mail.Header
	text, html  string
	partHeaders []map[string][]string
}

func parse(t *testing.T, raw []byte) parsed {
	t.Helper()
	if bytes.Contains(bytes.ReplaceAll(raw, []byte("\r\n"), nil), []byte("\n")) {
		t.Fatal("bare LF in the message; SMTP needs CRLF")
	}
	msg, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	mt, params, err := mime.ParseMediaType(msg.Header.Get("Content-Type"))
	if err != nil || mt != "multipart/alternative" {
		t.Fatalf("Content-Type %q %v", msg.Header.Get("Content-Type"), err)
	}
	p := parsed{header: msg.Header}
	mr := multipart.NewReader(msg.Body, params["boundary"])
	for i := 0; ; i++ {
		part, err := mr.NextRawPart()
		if errors.Is(err, io.EOF) {
			if i != 2 {
				t.Fatalf("%d parts, want 2", i)
			}
			return p
		}
		if err != nil {
			t.Fatal(err)
		}
		if cte := part.Header.Get("Content-Transfer-Encoding"); cte != "quoted-printable" {
			t.Fatalf("part %d Content-Transfer-Encoding %q", i, cte)
		}
		b, err := io.ReadAll(quotedprintable.NewReader(part))
		if err != nil {
			t.Fatal(err)
		}
		body := strings.ReplaceAll(string(b), "\r\n", "\n")
		p.partHeaders = append(p.partHeaders, part.Header)
		switch ct := part.Header.Get("Content-Type"); {
		case i == 0 && ct == "text/plain; charset=UTF-8":
			p.text = body
		case i == 1 && ct == "text/html; charset=UTF-8":
			p.html = body
		default:
			t.Fatalf("part %d is %q; text/plain must come first and text/html last (clients show the last part they understand)", i, ct)
		}
	}
}

func golden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", name+".golden")
	if *update {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run go test ./internal/mailer -update to create it)", err)
	}
	if got != string(want) {
		t.Errorf("%s differs from the golden file; if the change is intended, rerun with -update and review the diff\n--- got ---\n%s", path, got)
	}
}

var imgSrc = regexp.MustCompile(`<img [^>]*src="([^"]*)"`)

func TestEveryMessageMatchesItsGoldenFiles(t *testing.T) {
	for _, tc := range messages {
		t.Run(tc.kind, func(t *testing.T) {
			c := &capture{}
			if err := tc.send(testMailer(c), "ada@example.com"); err != nil {
				t.Fatal(err)
			}
			if c.kind != tc.kind || c.to != "ada@example.com" {
				t.Fatalf("delivered %q to %q", c.kind, c.to)
			}
			p := parse(t, c.raw)
			golden(t, tc.kind+".txt", p.text)
			golden(t, tc.kind+".html", p.html)

			// The kit's rules, checked on every message rather than trusted
			// to the golden review.
			if n := strings.Count(p.html, "#F5A623"); n != 0 && n != 2 { // the button cell and its VML twin
				t.Errorf("amber appears %d times", n)
			}
			srcs := imgSrc.FindAllStringSubmatch(p.html, -1)
			if len(srcs) != 2 {
				t.Errorf("%d images; the layout has exactly the lockup and the mark, and no tracking pixel", len(srcs))
			}
			for _, s := range srcs {
				if !strings.HasPrefix(s[1], "https://compute.unifyai.us/email/") {
					t.Errorf("image %q is not hosted on compute.unifyai.us/email/", s[1])
				}
			}
			for _, want := range []string{"Unify Compute · compute.unifyai.us", "© UnifyAI · Operated by FelixSphere LLC · 6 Karen Ct, CA 94010, United States"} {
				if !strings.Contains(p.text, want) {
					t.Errorf("text part lacks %q", want)
				}
			}
			if strings.Contains(p.html, "{{") || strings.Contains(p.text, "{{") {
				t.Error("an unfilled slot")
			}
			subject, err := new(mime.WordDecoder).DecodeHeader(p.header.Get("Subject"))
			if err != nil || !strings.Contains(p.html, "<title>"+strings.ReplaceAll(subject, "'", "&#39;")+"</title>") {
				t.Errorf("subject %q (%v) is not the HTML title", subject, err)
			}
		})
	}
}

func TestHeaders(t *testing.T) {
	c := &capture{}
	if err := testMailer(c).VerificationCode(context.Background(), "ada@example.com", "042917"); err != nil {
		t.Fatal(err)
	}
	h := parse(t, c.raw).header
	for k, want := range map[string]string{
		"From":           `"Unify Compute" <no-reply@compute.unifyai.us>`,
		"To":             "<ada@example.com>",
		"Subject":        "Your Unify Compute verification code",
		"Date":           "Sat, 03 Oct 2026 14:05:00 +0000",
		"Mime-Version":   "1.0",
		"Auto-Submitted": "auto-generated",
	} {
		if got := h.Get(k); got != want {
			t.Errorf("%s = %q, want %q", k, got, want)
		}
	}
	if !regexp.MustCompile(`^<[0-9a-f]{32}@compute\.unifyai\.us>$`).MatchString(h.Get("Message-Id")) {
		t.Errorf("Message-ID %q", h.Get("Message-Id"))
	}
}

// A key name is the one value in A2's messages a user types. It must not
// reach the HTML as markup, the text part as a new line, or the headers at
// all.
func TestUserChosenValuesCannotInject(t *testing.T) {
	c := &capture{}
	name := "evil</td><script>alert(1)</script><img src=\"https://tracker.example/p.gif\">\r\nBcc: victim@example.com\r\n\r\n--boundary"
	err := testMailer(c).KeyCreated(context.Background(), "ada@example.com", Key{Name: name, Display: "uck_AbCd1234", Scopes: []string{"jobs"}, CreatedAt: at})
	if err != nil {
		t.Fatal(err)
	}
	p := parse(t, c.raw)
	if p.header.Get("Bcc") != "" {
		t.Fatal("a Bcc header was injected")
	}
	if strings.Contains(p.html, "<script") || strings.Contains(p.html, "tracker.example/p.gif\">") || strings.Contains(p.html, "</td><script") {
		t.Fatalf("markup from the key name reached the HTML:\n%s", p.html)
	}
	if !strings.Contains(p.html, "evil&lt;/td&gt;&lt;script&gt;alert(1)&lt;/script&gt;") {
		t.Fatal("the key name is not shown escaped")
	}
	if len(imgSrc.FindAllString(p.html, -1)) != 2 {
		t.Fatal("an image was injected")
	}
	if !strings.Contains(p.text, "Name: evil</td><script>alert(1)</script><img src=\"https://tracker.example/p.gif\"> Bcc: victim@example.com --boundary\n") {
		t.Fatalf("the key name is not on one line in the text part:\n%s", p.text)
	}
	for _, line := range strings.Split(p.text, "\n") {
		if strings.HasPrefix(line, "Bcc:") || strings.HasPrefix(line, "--boundary") {
			t.Fatalf("the key name started a line: %q", line)
		}
	}
}

func TestRecipientMustBeABareAddress(t *testing.T) {
	for _, to := range []string{
		"ada@example.com\r\nBcc: victim@example.com",
		"ada@example.com\nBcc: victim@example.com",
		"Ada <ada@example.com>",
		"ada@example.com, eve@example.com",
		"not an address",
		"",
	} {
		c := &capture{}
		if err := testMailer(c).SignupAttempt(context.Background(), to); err == nil || c.raw != nil {
			t.Errorf("%q was accepted", to)
		}
	}
}

func TestResetTokenIsInTheFragment(t *testing.T) {
	c := &capture{}
	if err := testMailer(c).PasswordReset(context.Background(), "ada@example.com", "a+b/c="); err != nil {
		t.Fatal(err)
	}
	p := parse(t, c.raw)
	want := origin + "/reset-password#token=a%2Bb%2Fc%3D"
	if !strings.Contains(p.text, "Reset password:\n"+want+"\n") || !strings.Contains(p.html, `href="`+want+`"`) {
		t.Fatalf("reset link is not %s", want)
	}
}

func TestSendsSignedRawEmailThroughSES(t *testing.T) {
	f := awsv4test.New(t, "bkt")
	m, err := New(Settings{
		Sender: "Unify Compute <no-reply@compute.unifyai.us>", ConfigurationSet: "unify-compute",
		AppOrigin: origin, AccountsBackend: "local", Environment: "staging",
	}, f.Client(), awsv4.Static(f.Instance))
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Transport.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := m.VerificationCode(context.Background(), "success@simulator.amazonses.com", "042917"); err != nil {
		t.Fatal(err)
	}
	if len(f.Emails) != 1 {
		t.Fatalf("%d emails reached SES", len(f.Emails))
	}
	e := f.Emails[0]
	if e.From != "no-reply@compute.unifyai.us" || len(e.To) != 1 || e.To[0] != "success@simulator.amazonses.com" || e.ConfigurationSet != "unify-compute" {
		t.Fatalf("SendEmail %+v", e)
	}
	p := parse(t, e.Raw)
	if p.header.Get("To") != "<success@simulator.amazonses.com>" || !strings.Contains(p.text, "Your code: 042917") || !strings.Contains(p.html, ">042917</div>") {
		t.Fatalf("raw message:\n%s", e.Raw)
	}
}

func TestSESRefusalAndBadCredentialsAreErrors(t *testing.T) {
	f := awsv4test.New(t, "bkt")
	m, err := New(Settings{Sender: "Unify Compute <no-reply@compute.unifyai.us>", AppOrigin: origin}, f.Client(), awsv4.Static(f.Instance))
	if err != nil {
		t.Fatal(err)
	}
	f.SESError = "MessageRejected"
	err = m.SignupAttempt(context.Background(), "ada@example.com")
	var ae *awsv4.Error
	if !errors.As(err, &ae) || ae.Code != "MessageRejected" || ae.Status != 400 {
		t.Fatalf("err = %v", err)
	}
	f.SESError = ""
	bad := f.Instance
	bad.SecretAccessKey = "wrong"
	m.Transport.(*SES).Creds = awsv4.Static(bad)
	if err := m.SignupAttempt(context.Background(), "ada@example.com"); err == nil || len(f.Emails) != 0 {
		t.Fatalf("a badly signed request was accepted: %v", err)
	}
}

func TestNewRefusesLocalAccountsWithoutASender(t *testing.T) {
	cases := []struct {
		name    string
		s       Settings
		want    string // "" = nil mailer, "ses", "outbox", "error"
		errLike string
	}{
		{"console accounts, no sender: no mail", Settings{}, "", ""},
		{"local accounts, no sender, staging", Settings{AccountsBackend: "local", Environment: "staging", AppOrigin: origin}, "error", "needs SES_SENDER"},
		{"local accounts, no sender, ENVIRONMENT unset", Settings{AccountsBackend: "local", AppOrigin: origin}, "error", "needs SES_SENDER"},
		{"local accounts, no sender, ENVIRONMENT=local is not development", Settings{AccountsBackend: "local", Environment: "local", AppOrigin: origin}, "error", "needs SES_SENDER"},
		{"local accounts, development: outbox", Settings{AccountsBackend: "local", Environment: "development", AppOrigin: "http://localhost:3000"}, "outbox", ""},
		{"sender set: SES", Settings{Sender: "Unify Compute <no-reply@compute.unifyai.us>", AccountsBackend: "local", AppOrigin: origin}, "ses", ""},
		{"sender wins over development", Settings{Sender: "Unify Compute <no-reply@compute.unifyai.us>", Environment: "development", AppOrigin: origin}, "ses", ""},
		{"sender without a display name", Settings{Sender: "no-reply@compute.unifyai.us", AppOrigin: origin}, "error", "no display name"},
		{"sender unparseable (the .env quoting mistake)", Settings{Sender: "Unify Compute", AppOrigin: origin}, "error", "SES_SENDER"},
		{"no APP_ORIGIN", Settings{Sender: "Unify Compute <no-reply@compute.unifyai.us>"}, "error", "APP_ORIGIN"},
		{"http APP_ORIGIN outside development", Settings{Sender: "Unify Compute <no-reply@compute.unifyai.us>", AppOrigin: "http://app.compute.unifyai.us"}, "error", "APP_ORIGIN"},
		{"APP_ORIGIN with a path", Settings{Sender: "Unify Compute <no-reply@compute.unifyai.us>", AppOrigin: origin + "/x"}, "error", "APP_ORIGIN"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m, err := New(tc.s, &awsv4.Client{Region: "us-east-1"}, awsv4.Static{})
			var got string
			switch {
			case err != nil:
				got = "error"
				if !strings.Contains(err.Error(), tc.errLike) {
					t.Errorf("err %q lacks %q", err, tc.errLike)
				}
			case m == nil:
				got = ""
			default:
				switch m.Transport.(type) {
				case *SES:
					got = "ses"
				case *Outbox:
					got = "outbox"
				}
			}
			if got != tc.want {
				t.Fatalf("got %q (err %v), want %q", got, err, tc.want)
			}
		})
	}
}

func TestSettingsFromEnvTrimsAndReadsTheSpecNames(t *testing.T) {
	env := map[string]string{
		"SES_SENDER": " Unify Compute <no-reply@compute.unifyai.us> ", "SES_CONFIGURATION_SET": "unify-compute",
		"APP_ORIGIN": "https://app.compute.unifyai.us/", "ACCOUNTS_BACKEND": "local", "ENVIRONMENT": "staging",
		"MAIL_OUTBOX_DIR": "/tmp/x",
	}
	s := SettingsFromEnv(func(k string) string { return env[k] })
	want := Settings{Sender: "Unify Compute <no-reply@compute.unifyai.us>", ConfigurationSet: "unify-compute",
		AppOrigin: "https://app.compute.unifyai.us", OutboxDir: "/tmp/x", AccountsBackend: "local", Environment: "staging"}
	if s != want || !s.Required() {
		t.Fatalf("%+v", s)
	}
}

func TestOutboxWritesPrivateEMLFiles(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "outbox")
	m, err := New(Settings{AccountsBackend: "local", Environment: "development", AppOrigin: "http://localhost:3000", OutboxDir: dir}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Transport.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := m.PasswordReset(context.Background(), "ada@example.com", "tok"); err != nil {
		t.Fatal(err)
	}
	files, _ := filepath.Glob(filepath.Join(dir, "*-password-reset-*.eml"))
	if len(files) != 1 {
		t.Fatalf("outbox has %v", files)
	}
	st, _ := os.Stat(files[0])
	if st.Mode().Perm() != 0o600 {
		t.Errorf("mode %v", st.Mode().Perm())
	}
	raw, _ := os.ReadFile(files[0])
	if p := parse(t, raw); !strings.Contains(p.text, "http://localhost:3000/reset-password#token=tok") {
		t.Fatalf("outbox message:\n%s", p.text)
	}
}
