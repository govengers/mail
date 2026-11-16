package mail

import (
	"errors"
	"fmt"
	"mime"
	netmail "net/mail"
	"strings"
)

// MaximumIdempotencyKeyLength is the longest IdempotencyKey a Message may carry (Resend's limit).
const MaximumIdempotencyKeyLength = 256

var (
	// ErrPermanent marks a failure that sending the same message again cannot fix: an invalid
	// message, a sender, recipient or content the server rejected. A misconfigured transport (a
	// refused password or API key) is not permanent: the message goes out once it is fixed.
	// Transports wrap it; match it with errors.Is to stop retrying. Every other error may be
	// temporary.
	ErrPermanent = errors.New("mail: permanent failure")
	// ErrInvalidMessage is returned for a message that fails Validate. It always comes with
	// ErrPermanent.
	ErrInvalidMessage = errors.New("mail: invalid message")
)

// Message is one mail. Addresses are RFC 5322 addresses, with or without a display name
// ("Ada <ada@example.com>" or "ada@example.com"). Transports read a Message and never change it.
// The JSON form is part of jobsadapter's job payload.
type Message struct {
	From    string   `json:"from,omitempty"` // empty takes the Mailer's Options.From
	To      []string `json:"to"`             // at least one
	ReplyTo string   `json:"reply_to,omitempty"`
	Subject string   `json:"subject"`
	HTML    string   `json:"html,omitempty"` // HTML and Text are the two alternatives; at least one is set
	Text    string   `json:"text,omitempty"`
	// Headers are extra header fields, such as List-Unsubscribe. They may not set the fields the
	// transports write themselves (From, To, Subject, Content-Type, ...).
	Headers     map[string]string `json:"headers,omitempty"`
	Attachments []Attachment      `json:"attachments,omitempty"`
	// IdempotencyKey makes a retried send deliver once on transports that support it (resend, for
	// 24 hours). smtp derives the Message-ID from it, so a duplicate is recognisable; logmailer and
	// mailtest ignore it. jobsadapter sets it to the job's ID.
	IdempotencyKey string `json:"idempotency_key,omitempty"`
}

// Attachment is a file sent with a Message.
type Attachment struct {
	Filename    string `json:"filename"`               // a file name without a path
	ContentType string `json:"content_type,omitempty"` // empty means application/octet-stream
	Content     []byte `json:"content"`
}

// reservedHeaders are the fields the transports write from the Message's own fields.
var reservedHeaders = map[string]bool{
	"bcc": true, "cc": true, "content-disposition": true, "content-transfer-encoding": true,
	"content-type": true, "date": true, "from": true, "message-id": true, "mime-version": true,
	"reply-to": true, "return-path": true, "sender": true, "subject": true, "to": true,
}

// Validate reports the first problem of the message as an error that matches ErrInvalidMessage
// and ErrPermanent, or nil. The Mailer calls it before every send.
func (m *Message) Validate() error {
	if err := m.validateAddresses(); err != nil {
		return invalid(err)
	}
	if m.Subject == "" || strings.ContainsAny(m.Subject, "\r\n") {
		return invalid(errors.New("the subject is empty or spans several lines"))
	}
	if m.HTML == "" && m.Text == "" {
		return invalid(errors.New("neither an HTML nor a text part"))
	}
	if len(m.IdempotencyKey) > MaximumIdempotencyKeyLength {
		return invalid(fmt.Errorf("the idempotency key is longer than %d bytes", MaximumIdempotencyKeyLength))
	}
	for name, value := range m.Headers {
		if err := validateHeader(name, value); err != nil {
			return invalid(err)
		}
	}
	for index := range m.Attachments {
		if err := m.Attachments[index].validate(); err != nil {
			return invalid(fmt.Errorf("attachment %d: %w", index, err))
		}
	}
	return nil
}

func (m *Message) validateAddresses() error {
	if err := validateAddress(m.From); err != nil {
		return fmt.Errorf("from %q: %w", m.From, err)
	}
	if len(m.To) == 0 {
		return errors.New("no recipient")
	}
	for _, recipient := range m.To {
		if err := validateAddress(recipient); err != nil {
			return fmt.Errorf("to %q: %w", recipient, err)
		}
	}
	if m.ReplyTo == "" {
		return nil
	}
	if err := validateAddress(m.ReplyTo); err != nil {
		return fmt.Errorf("reply-to %q: %w", m.ReplyTo, err)
	}
	return nil
}

// validateAddress accepts an RFC 5322 address whose addr-spec can go into an SMTP envelope as it
// is. net/mail unquotes a quoted local part, and net/smtp writes MAIL FROM:<%s> and RCPT TO:<%s>
// unescaped, so a local part with a space or a '>' would end the path and append parameters
// ("a@evil.example> NOTIFY=NEVER"@corp.example), and one with '@' or a misplaced dot would reach
// another mailbox. Quoted local parts that need their quotes and domain literals are therefore
// rejected: net/mail's String quotes exactly the local parts that need it, so a round trip that
// changes the addr-spec finds them.
func validateAddress(value string) error {
	address, err := netmail.ParseAddress(value)
	if err != nil {
		return errors.New(err.Error()) // LIB-010: net/mail's parse errors are not part of the API
	}
	if (&netmail.Address{Address: address.Address}).String() != "<"+address.Address+">" || strings.Contains(address.Address, "[") {
		return errors.New("a quoted local part or a domain literal is not supported")
	}
	return nil
}

func validateHeader(name, value string) error {
	if name == "" || strings.IndexFunc(name, func(r rune) bool { return r <= ' ' || r > '~' || r == ':' }) >= 0 {
		return fmt.Errorf("header name %q is not a field name", name)
	}
	if reservedHeaders[strings.ToLower(name)] {
		return fmt.Errorf("header %s is set from the message's fields", name)
	}
	if strings.ContainsAny(value, "\r\n") {
		return fmt.Errorf("header %s spans several lines", name)
	}
	return nil
}

func (a *Attachment) validate() error {
	if a.Filename == "" || strings.ContainsAny(a.Filename, "/\\\r\n\x00") {
		return fmt.Errorf("file name %q is empty or contains a path or a line break", a.Filename)
	}
	if a.ContentType == "" {
		return nil
	}
	if strings.ContainsAny(a.ContentType, "\r\n") {
		return fmt.Errorf("content type %q spans several lines", a.ContentType)
	}
	if _, _, err := mime.ParseMediaType(a.ContentType); err != nil {
		return fmt.Errorf("content type %q: %v", a.ContentType, err) //nolint:errorlint // LIB-010
	}
	return nil
}

func invalid(err error) error {
	return fmt.Errorf("%w: %w: %w", ErrPermanent, ErrInvalidMessage, err)
}
