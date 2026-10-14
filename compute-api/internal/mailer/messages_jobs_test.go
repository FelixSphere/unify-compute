package mailer

import (
	"context"
	"mime"
	"strings"
	"testing"
)

var a10g = JobOutcome{ID: "job_01J8Q6ZK3R7Y9W2X4V6T8S0A1B", SKU: "a10g-24gb-x1", GPU: "NVIDIA A10G", GPUSeconds: 1830, CostUSD: "0.6100"}

// The W2 messages, with the arguments their golden files were made from.
// Golden name is the case name; kind is what the transport is told.
var jobMessages = []struct {
	name, kind string
	send       func(m *Mailer, to string) error
}{
	{"job-succeeded", KindJobSucceeded, func(m *Mailer, to string) error { return m.JobSucceeded(context.Background(), to, a10g) }},
	{"job-failed-after-up", KindJobFailed, func(m *Mailer, to string) error {
		j := a10g
		j.ErrorCode, j.Started = "nonzero_exit", true
		return m.JobFailed(context.Background(), to, j)
	}},
	{"job-failed-before-up", KindJobFailed, func(m *Mailer, to string) error {
		j := a10g
		j.ErrorCode, j.GPUSeconds, j.CostUSD = "capacity_exhausted", 0, "0.0000"
		return m.JobFailed(context.Background(), to, j)
	}},
	{"job-billing-stopped-balance", KindJobBillingStopped, func(m *Mailer, to string) error {
		j := a10g
		j.ErrorCode, j.Started = "balance_exhausted", true
		return m.JobBillingStopped(context.Background(), to, j)
	}},
	{"job-billing-stopped-unavailable", KindJobBillingStopped, func(m *Mailer, to string) error {
		j := a10g
		j.ErrorCode, j.Started = "billing_unavailable", true
		return m.JobBillingStopped(context.Background(), to, j)
	}},
	{"topup-received", KindTopupReceived, func(m *Mailer, to string) error {
		return m.TopupReceived(context.Background(), to, TopupReceipt{ID: "tu_01J8Q6ZK3R7Y9W2X4V6T8S0A1B", AmountUSD: "20.00", BalanceUSD: "23.4500",
			PaidAt: at, ReceiptURL: "https://pay.stripe.com/receipts/payment/CAcaFwoVYWNjdF8xTjQ"})
	}},
	{"topup-received-no-receipt", KindTopupReceived, func(m *Mailer, to string) error {
		return m.TopupReceived(context.Background(), to, TopupReceipt{ID: "tu_01J8Q6ZK3R7Y9W2X4V6T8S0A1B", AmountUSD: "20.00", BalanceUSD: "20.0000", PaidAt: at})
	}},
}

func TestJobAndTopupMessagesMatchTheirGoldenFiles(t *testing.T) {
	for _, tc := range jobMessages {
		t.Run(tc.name, func(t *testing.T) {
			c := &capture{}
			if err := tc.send(testMailer(c), "ada@example.com"); err != nil {
				t.Fatal(err)
			}
			if c.kind != tc.kind || c.to != "ada@example.com" {
				t.Fatalf("delivered %q to %q", c.kind, c.to)
			}
			p := parse(t, c.raw)
			golden(t, tc.name+".txt", p.text)
			golden(t, tc.name+".html", p.html)

			if n := strings.Count(p.html, "#F5A623"); n != 2 {
				t.Errorf("amber appears %d times; exactly the one button and its VML twin", n)
			}
			if len(imgSrc.FindAllString(p.html, -1)) != 2 {
				t.Error("the layout has exactly two images")
			}
			if strings.Contains(p.html, "{{") || strings.Contains(p.text, "{{") {
				t.Error("an unfilled slot")
			}
			subject, err := new(mime.WordDecoder).DecodeHeader(p.header.Get("Subject"))
			if err != nil || !strings.Contains(p.html, "<title>"+strings.ReplaceAll(subject, "'", "&#39;")+"</title>") {
				t.Errorf("subject %q (%v) is not the HTML title", subject, err)
			}
			if strings.Contains(p.text, "Unify API") || strings.Contains(p.html, "Unify API") {
				t.Error("Compute's own accounts have no Unify API console to point at")
			}
		})
	}
}

// The job emails link the job page on APP_ORIGIN, never a fixed host.
func TestJobLinkUsesAppOrigin(t *testing.T) {
	c := &capture{}
	m := testMailer(c)
	m.AppOrigin = "https://app.staging.compute.unifyai.us"
	if err := m.JobSucceeded(context.Background(), "ada@example.com", a10g); err != nil {
		t.Fatal(err)
	}
	p := parse(t, c.raw)
	want := "https://app.staging.compute.unifyai.us/jobs/" + a10g.ID
	if !strings.Contains(p.text, "View job:\n"+want+"\n") || !strings.Contains(p.html, `href="`+want+`"`) {
		t.Fatalf("job link is not %s:\n%s", want, p.text)
	}
}

func TestStatusRowCarriesTheKitDot(t *testing.T) {
	for _, tc := range []struct {
		send   func(m *Mailer) error
		word   string
		colour string
	}{
		{func(m *Mailer) error { return m.JobSucceeded(context.Background(), "ada@example.com", a10g) }, "succeeded", "#1E9E63"},
		{func(m *Mailer) error { return m.JobFailed(context.Background(), "ada@example.com", a10g) }, "failed", "#D92D20"},
		{func(m *Mailer) error { return m.JobBillingStopped(context.Background(), "ada@example.com", a10g) }, "cancelled", "#E0A100"},
	} {
		c := &capture{}
		if err := tc.send(testMailer(c)); err != nil {
			t.Fatal(err)
		}
		p := parse(t, c.raw)
		if !strings.Contains(p.html, `<span style="color:`+tc.colour+`;">&#9679;</span>&nbsp;`+tc.word+`</td>`) {
			t.Errorf("%s: no %s dot beside the status word", tc.word, tc.colour)
		}
		if !strings.Contains(p.text, "\nStatus: "+tc.word+"\nJob: ") {
			t.Errorf("%s: text part lacks the status row first:\n%s", tc.word, p.text)
		}
	}
}

// A receipt URL that is not https is not linked; the button opens Billing.
func TestTopupReceiptLinkMustBeHTTPS(t *testing.T) {
	for _, u := range []string{"", "javascript:alert(1)", "http://pay.stripe.com/r", "https://"} {
		c := &capture{}
		if err := testMailer(c).TopupReceived(context.Background(), "ada@example.com", TopupReceipt{ID: "tu_x", AmountUSD: "5.00", BalanceUSD: "5.0000", PaidAt: at, ReceiptURL: u}); err != nil {
			t.Fatal(err)
		}
		p := parse(t, c.raw)
		if !strings.Contains(p.text, "View billing:\n"+origin+"/billing\n") || (u != "" && strings.Contains(p.html, u+`"`)) {
			t.Errorf("receipt %q:\n%s", u, p.text)
		}
	}
}

func TestEveryFailureCodeHasAPlainLine(t *testing.T) {
	for _, code := range []string{"capacity_exhausted", "image_pull_failed", "provision_failed", "nonzero_exit", "max_runtime_exceeded", "preempted", "provider_fault"} {
		if FailureLine(code) == FailureLine("something_new") {
			t.Errorf("%s falls back to the generic line", code)
		}
	}
}
