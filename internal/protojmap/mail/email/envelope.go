package email

import (
	"net/mail"
	"strings"
	"time"

	"github.com/hanshuebner/herold/internal/mailparse"
	"github.com/hanshuebner/herold/internal/store"
)

// buildEnvelopeFromParsed extracts the cached envelope columns from a
// successfully parsed mailparse.Message. On parse failure or missing
// fields the returned Envelope has zero values for the absent slots —
// the JMAP wire form uses empty strings, which we copy through.
func buildEnvelopeFromParsed(m mailparse.Message) store.Envelope {
	env := store.Envelope{
		Subject:   m.Envelope.Subject,
		MessageID: m.Envelope.MessageID,
		From:      addrListString(m.Envelope.From),
		To:        addrListString(m.Envelope.To),
		Cc:        addrListString(m.Envelope.Cc),
		Bcc:       addrListString(m.Envelope.Bcc),
		ReplyTo:   addrListString(m.Envelope.ReplyTo),
	}
	if len(m.Envelope.InReplyTo) > 0 {
		env.InReplyTo = m.Envelope.InReplyTo[0]
	}
	// Store the raw References value so InsertMessage can use it for
	// thread resolution via mailparse.ParseReferences. We re-bracket the
	// already-normalized IDs because ParseReferences expects angle-bracket
	// tokens.
	if len(m.Envelope.References) > 0 {
		parts := make([]string, len(m.Envelope.References))
		for i, r := range m.Envelope.References {
			parts[i] = "<" + r + ">"
		}
		env.References = strings.Join(parts, " ")
	}
	if m.Envelope.Date != "" {
		if t, err := mail.ParseDate(m.Envelope.Date); err == nil {
			env.Date = t.UTC()
		} else if t, err := time.Parse(time.RFC1123Z, m.Envelope.Date); err == nil {
			env.Date = t.UTC()
		}
	}
	return env
}

func addrListString(xs []mail.Address) string {
	if len(xs) == 0 {
		return ""
	}
	parts := make([]string, 0, len(xs))
	for _, a := range xs {
		parts = append(parts, a.String())
	}
	return strings.Join(parts, ", ")
}
