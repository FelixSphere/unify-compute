package mailer

import (
	"context"
	"time"
)

const pathSupport = "/support"

// SupportAnswer is a staff reply in a support conversation (ADR-0014).
type SupportAnswer struct {
	ConversationID string    `json:"conversation_id"`
	At             time.Time `json:"at"`
}

// SupportReply tells the user who wrote to support that there is an answer.
// The answer itself is not in the email: it is read, and replied to, on the
// Support page, so the conversation stays in one place.
func (m *Mailer) SupportReply(ctx context.Context, to string, a SupportAnswer) error {
	return m.send(ctx, to, &email{
		Kind:         "support-reply",
		Subject:      "Unify Compute support replied to you",
		Preheader:    "There is a new reply in your support conversation.",
		Eyebrow:      "SUPPORT",
		Headline:     "We replied to your message.",
		Intro:        "There is a new reply from the Unify Compute team in your support conversation. Read it, and write back if you need to, on the Support page.",
		CTA:          &cta{Label: "Read the reply", URL: m.AppOrigin + pathSupport},
		Details:      []detail{{"Conversation", oneLine(a.ConversationID)}, {"Replied", utc(a.At)}},
		Safety:       "Replies to this email are not read. If you didn't write to Unify Compute support, contact us at " + contactEmail + ".",
		FooterReason: reasonAccount,
	})
}
