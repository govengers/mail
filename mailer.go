package mail

import (
	"context"
	"errors"
	"fmt"
	netmail "net/mail"
	"time"
)

// Transport delivers a validated Message: smtp.Transport, resend.Transport, logmailer.Transport,
// mailtest.Recorder. A Transport must not change the message and must be safe for concurrent use.
// It returns an error matching ErrPermanent when sending the message again cannot succeed.
type Transport interface {
	Send(ctx context.Context, message *Message) error
}

// Options configures a Mailer.
type Options struct {
	Transport Transport // required
	// From is the sender of every message without its own From, e.g. "Example <noreply@example.com>".
	From     string
	Observer Observer // nil reports nothing
}

// Mailer validates messages, fills in the default sender and hands them to its Transport. It is
// itself a Transport and safe for concurrent use.
type Mailer struct {
	transport Transport
	from      string
	observer  Observer
}

// New returns a Mailer. It fails without a Transport and for a From that is not an address.
func New(options *Options) (*Mailer, error) {
	if options == nil || options.Transport == nil {
		return nil, errors.New("mail: Options.Transport is required")
	}
	if options.From != "" {
		if _, err := netmail.ParseAddress(options.From); err != nil {
			return nil, fmt.Errorf("mail: Options.From %q: %v", options.From, err) //nolint:errorlint // LIB-010
		}
	}
	return &Mailer{transport: options.Transport, from: options.From, observer: options.Observer}, nil
}

// Send validates the message and sends it through the transport; the caller's message is not
// changed. An invalid message is not sent and returns an error matching ErrInvalidMessage and
// ErrPermanent; transport errors are returned as they are.
func (m *Mailer) Send(ctx context.Context, message *Message) error {
	if message == nil {
		return invalid(errors.New("nil message"))
	}
	resolved := *message
	if resolved.From == "" {
		resolved.From = m.from
	}
	started := time.Now()
	err := resolved.Validate()
	if err == nil {
		err = m.transport.Send(ctx, &resolved)
	}
	if m.observer != nil {
		m.observer.SendFinished(&Outcome{
			Message:  &resolved,
			Result:   resultOf(err),
			Duration: time.Since(started),
			Err:      err,
		})
	}
	return err
}

func resultOf(err error) Result {
	switch {
	case err == nil:
		return ResultSent
	case errors.Is(err, ErrInvalidMessage):
		return ResultInvalid
	case errors.Is(err, ErrPermanent):
		return ResultRejected
	default:
		return ResultFailed
	}
}
