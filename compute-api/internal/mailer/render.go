package mailer

import (
	_ "embed"
	"html"
	"html/template"
	"strings"
)

// The kit's slots that are the same in every Unify Compute message
// (notifications.md, "Shared slots"). The images are served by the
// marketing site's site container from public/email/.
const (
	ProductName   = "Unify Compute"
	productShort  = "Compute"
	productURL    = "https://compute.unifyai.us"
	productDomain = "compute.unifyai.us"
	assetBase     = "https://compute.unifyai.us/email"
	company       = "UnifyAI · Lebuh Bandar Utama PJU 6, 47800 Petaling Jaya, Selangor, Malaysia"
	contactEmail  = "contactus@felixsphere.com"

	reasonAccount = "You're receiving this because this address is registered for a Unify Compute account."
	reasonSignup  = "You're receiving this because someone entered this address on the Unify Compute sign-up page."
)

// email is one message's content. The plain-text part and the HTML part
// are both rendered from it, so they cannot say different things. At most
// one of CTA and Code is set: the kit allows one focal element.
type email struct {
	Kind         string
	Subject      string
	Preheader    string
	Eyebrow      string
	Headline     string
	Intro        string
	CTA          *cta
	Code         string
	Details      []detail
	Safety       string
	FooterReason string
}

type cta struct{ Label, URL string }

type detail struct{ Label, Value string }

//go:embed layout.html
var layoutHTML string

// html/template drops comments from template text, and Outlook's
// conditional comments are how the kit gives it a VML button and hides the
// web fonts from it. They are emitted as values instead, with everything
// interpolated into them escaped here.
var layout = template.Must(template.New("layout").Funcs(template.FuncMap{
	"notMSO":    func() template.HTML { return "<!--[if !mso]><!-->" },
	"endNotMSO": func() template.HTML { return "<!--<![endif]-->" },
	"msoButton": func(c *cta) template.HTML {
		return template.HTML(`<!--[if mso]><v:roundrect xmlns:v="urn:schemas-microsoft-com:vml" href="` + html.EscapeString(c.URL) +
			`" style="height:48px;v-text-anchor:middle;width:240px;" arcsize="12%" stroke="f" fillcolor="#F5A623"><center style="color:#0E0E0E;font-family:Arial,sans-serif;font-size:16px;font-weight:bold;">` +
			html.EscapeString(c.Label) + `</center></v:roundrect><![endif]-->`)
	},
}).Parse(layoutHTML))

func (e *email) html() (string, error) {
	var b strings.Builder
	err := layout.Execute(&b, struct {
		*email
		ProductName, ProductShort, ProductURL, ProductDomain, AssetBase, Company string
	}{e, ProductName, productShort, productURL, productDomain, assetBase, company})
	return b.String(), err
}

// text is the plain-text part, in the order the kit gives: eyebrow,
// headline, intro, the code or the CTA label with the URL on its own line,
// the details as "Label: value", the safety line, the footer.
func (e *email) text() string {
	var b strings.Builder
	p := func(s string) { b.WriteString(s + "\n\n") }
	p(e.Eyebrow)
	p(e.Headline)
	p(e.Intro)
	switch {
	case e.CTA != nil:
		p(e.CTA.Label + ":\n" + e.CTA.URL)
	case e.Code != "":
		p("Your code: " + e.Code)
	}
	if len(e.Details) > 0 {
		var rows []string
		for _, d := range e.Details {
			rows = append(rows, d.Label+": "+d.Value)
		}
		p(strings.Join(rows, "\n"))
	}
	p(e.Safety)
	b.WriteString("-- \n" + ProductName + " · " + productDomain + "\n" + e.FooterReason + "\n" + company + "\n")
	return b.String()
}
