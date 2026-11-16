// Package jobsadapter sends mail from a govengers/jobs job, so a mail is enqueued in the same
// transaction as the change that causes it and goes out after the commit, with retries. It is a
// module of its own, so the mail module does not depend on govengers/jobs.
//
// The job payload is a reference, not the mail: the template name, the locale, the envelope (the
// recipients) and the template data. The handler renders the mail when the job runs, so the
// rendered mail, with the bearer links it carries (password reset, invitation), never sits in the
// jobs table or its admin listing. Template data that is itself a secret (a reset token) is
// sealed with Options.Sealer, or replaced by a reference that Options.Resolve turns back into
// template data when the job runs.
//
//	sender, err := jobsadapter.New(&jobsadapter.Options{Transport: mailer, Templates: templates, Sealer: sealer})
//	err = sender.Handle(worker, jobsadapter.Kind)                                      // main
//	payload, err := sender.Template("reset", user.Locale, data, &mail.Message{To: []string{user.Email}})
//	_, err = store.Enqueue(ctx, transaction, jobsadapter.Kind.New(payload, nil))       // in the caller's transaction
//
// A mail without secrets can still be rendered by the caller and sent as it is:
// jobsadapter.Kind.New(jobsadapter.Payload{Message: *message}, nil). The payload then holds the
// whole message.
//
// A service that needs another queue, attempt count or backoff declares its own
// jobs.NewKind[jobsadapter.Payload] and passes it instead.
//
// A job attempt sends with the job's ID as the message's IdempotencyKey unless the message has
// one, so the resend transport delivers once across retries (within Resend's 24 hours). The smtp
// transport has no such key: a retry after a lost reply delivers again, so SMTP is at least once;
// its Message-ID follows the key, so receivers that deduplicate on it drop the copy.
// A failure that matches mail.ErrPermanent (an invalid message, a rejected recipient) or a render
// error ends the job as dead at once; every other failure retries with the kind's backoff.
//
// govengers/jobs caps a payload at 256 KiB, so large attachments belong in blob storage,
// referenced from the template data and resolved by Options.Resolve.
package jobsadapter

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/govengers/jobs"

	"github.com/govengers/mail"
)

// Kind is the default kind for outgoing mail: name "mail.send" on the queue "mail", with jobs'
// default attempts and backoff. A kind is a value without state, so sharing it is safe.
var Kind = jobs.NewKind[Payload]("mail.send", &jobs.KindOptions{Queue: "mail"})

// Payload is the payload of a mail job: a template mail rendered when the job runs, or, with an
// empty Template, a message rendered by the caller and sent as it is. Build a template mail's
// payload with Sender.Template, which encodes and seals the data and checks that it renders.
type Payload struct {
	Template string `json:"template,omitempty"` // the template name; empty sends Message as it is
	Locale   string `json:"locale,omitempty"`   // the requested locale; Render falls back from it
	// Data is the template data as JSON in clear text; SealedData is the same sealed by the
	// Sealer. A template mail carries one of the two.
	Data       json.RawMessage `json:"data,omitempty"`
	SealedData []byte          `json:"sealed_data,omitempty"`
	// Message is the envelope of a template mail (To, From, ReplyTo, Headers, Attachments,
	// IdempotencyKey; the rendered Subject, HTML and Text replace its own) or the whole message
	// without a template.
	Message mail.Message `json:"message"`
}

// Sealer encrypts the template data of a payload at rest. associatedData binds the sealed data to
// the payload's template, locale and recipients: Open fails for data moved to another job. It has
// the method set of govengers/events' Sealer; the seal package of github.com/govengers/auth is
// wrapped in a few lines, see docs/wiki/Jobs.md.
type Sealer interface {
	Seal(plaintext, associatedData []byte) ([]byte, error)
	Open(sealed, associatedData []byte) ([]byte, error)
}

// ResolveFunc turns a payload's decoded template data into the data the template renders, when
// the job runs: it loads what a reference in the data points to (a reset token by the ID of its
// request), or returns data unchanged for a template that carries its own data. An error that
// matches mail.ErrPermanent or is a jobs.Permanent ends the job (the token was used or revoked);
// every other error retries.
type ResolveFunc func(ctx context.Context, template string, data any) (any, error)

// Options configures a Sender.
type Options struct {
	Transport mail.Transport  // required; usually the service's *mail.Mailer, so validation and metrics apply
	Templates *mail.Templates // renders template mails; nil allows messages rendered by the caller only
	Sealer    Sealer          // seals the template data of every template mail; nil stores it in clear text
	Resolve   ResolveFunc     // nil renders the stored data as it is
}

// Sender builds the payloads of mail jobs and handles them. It is safe for concurrent use.
type Sender struct {
	transport mail.Transport
	templates *mail.Templates
	sealer    Sealer
	resolve   ResolveFunc
}

// New returns a Sender. It fails without a Transport.
func New(options *Options) (*Sender, error) {
	if options == nil || options.Transport == nil {
		return nil, errors.New("jobsadapter: Options.Transport is required")
	}
	return &Sender{transport: options.Transport, templates: options.Templates, sealer: options.Sealer, resolve: options.Resolve}, nil
}

// Template returns the payload of the template mail name in locale with data, sent to envelope's
// recipients. data is stored as JSON, so the template sees its JSON form (field names as their
// JSON keys, numbers as json.Number); it is sealed when the Sender has a Sealer. Without Resolve
// the JSON form is rendered once now, so a template or data that does not render fails here, in
// the caller's transaction, instead of in the job. Subject, HTML and Text of envelope are not
// stored.
func (s *Sender) Template(name, locale string, data any, envelope *mail.Message) (Payload, error) {
	if s.templates == nil {
		return Payload{}, errors.New("jobsadapter: a template mail needs Options.Templates")
	}
	if envelope == nil || len(envelope.To) == 0 {
		return Payload{}, errors.New("jobsadapter: a template mail needs an envelope with a recipient")
	}
	encoded, err := json.Marshal(data)
	if err != nil {
		return Payload{}, fmt.Errorf("jobsadapter: encode the data of %s: %w", name, err)
	}
	if s.resolve == nil {
		decoded, err := decode(encoded)
		if err != nil {
			return Payload{}, err
		}
		if _, err := s.templates.Render(name, locale, decoded); err != nil {
			return Payload{}, fmt.Errorf("jobsadapter: %w", err)
		}
	}
	payload := Payload{Template: name, Locale: locale, Message: *envelope}
	payload.Message.Subject, payload.Message.HTML, payload.Message.Text = "", "", ""
	if s.sealer == nil {
		payload.Data = encoded
		return payload, nil
	}
	if payload.SealedData, err = s.sealer.Seal(encoded, payload.associatedData()); err != nil {
		return Payload{}, fmt.Errorf("jobsadapter: seal the data of %s: %w", name, err)
	}
	return payload, nil
}

// Handle registers the Sender's handler of kind on worker.
func (s *Sender) Handle(worker *jobs.Worker, kind *jobs.Kind[Payload]) error {
	return jobs.Handle(worker, kind, s.Handler())
}

// Handler is the job handler Handle registers, for a worker that wraps it in its own handler. It
// renders a template mail and sends it with the Sender's transport.
func (s *Sender) Handler() func(ctx context.Context, job *jobs.Job[Payload]) error {
	return func(ctx context.Context, job *jobs.Job[Payload]) error {
		message, err := s.message(ctx, &job.Payload)
		if err != nil {
			return err
		}
		if message.IdempotencyKey == "" {
			message.IdempotencyKey = job.ID.String()
		}
		err = s.transport.Send(ctx, message)
		if errors.Is(err, mail.ErrPermanent) {
			return jobs.Permanent(err)
		}
		return err
	}
}

// message returns the payload's message, rendered for a template mail. A worker that cannot render
// yet (no Templates, a template it does not know during a rolling deploy) retries; data that does
// not render is permanent.
func (s *Sender) message(ctx context.Context, payload *Payload) (*mail.Message, error) {
	message := payload.Message
	if payload.Template == "" {
		return &message, nil
	}
	if s.templates == nil {
		return nil, errors.New("jobsadapter: a template mail needs Options.Templates")
	}
	data, err := s.data(ctx, payload)
	if err != nil {
		return nil, err
	}
	rendered, err := s.templates.Render(payload.Template, payload.Locale, data)
	if errors.Is(err, mail.ErrTemplateNotFound) {
		return nil, fmt.Errorf("jobsadapter: %w", err)
	}
	if err != nil {
		return nil, jobs.Permanent(fmt.Errorf("jobsadapter: %w", err))
	}
	message.Subject, message.HTML, message.Text = rendered.Subject, rendered.HTML, rendered.Text
	return &message, nil
}

// data returns the template data of a template mail: opened, decoded and resolved. A worker
// without the Sealer or the sealing key retries, so a key that arrives with the next rollout still
// sends the mail; data that does not decode is permanent.
func (s *Sender) data(ctx context.Context, payload *Payload) (any, error) {
	encoded := payload.Data
	if payload.SealedData != nil {
		if s.sealer == nil {
			return nil, errors.New("jobsadapter: sealed template data needs Options.Sealer")
		}
		opened, err := s.sealer.Open(payload.SealedData, payload.associatedData())
		if err != nil {
			return nil, fmt.Errorf("jobsadapter: open the data of %s: %w", payload.Template, err)
		}
		encoded = opened
	}
	data, err := decode(encoded)
	if err != nil {
		return nil, jobs.Permanent(err)
	}
	if s.resolve == nil {
		return data, nil
	}
	resolved, err := s.resolve(ctx, payload.Template, data)
	if errors.Is(err, mail.ErrPermanent) {
		return nil, jobs.Permanent(err)
	}
	return resolved, err
}

// associatedData binds sealed data to the payload's template, locale and recipients.
func (p *Payload) associatedData() []byte {
	associatedData, _ := json.Marshal([]any{"govengers_mail", p.Template, p.Locale, p.Message.To}) // strings always encode
	return associatedData
}

// decode reads template data with numbers as json.Number, so an ID renders with every digit.
// Empty data decodes as null.
func decode(encoded []byte) (any, error) {
	if len(encoded) == 0 {
		encoded = []byte("null")
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.UseNumber()
	var data any
	if err := decoder.Decode(&data); err != nil {
		return nil, fmt.Errorf("jobsadapter: decode the template data: %w", err)
	}
	return data, nil
}
