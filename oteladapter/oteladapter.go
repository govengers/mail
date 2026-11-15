// Package oteladapter traces sends with OpenTelemetry: NewTransport wraps a mail.Transport so every
// send is a client span in the caller's trace. It depends on the OpenTelemetry API only; the
// service builds the SDK provider (github.com/govengers/telemetry/tracing).
package oteladapter

import (
	"context"
	"errors"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/govengers/mail"
)

// ScopeName is the instrumentation scope of the send spans.
const ScopeName = "github.com/govengers/mail/oteladapter"

// SpanName is the name of every send span.
const SpanName = "mail.send"

// The span attributes. Addresses, subjects and error texts (which quote addresses) are personal
// data and never recorded.
const (
	AttributeTransport   = "mail.transport"
	AttributeRecipients  = "mail.recipients"
	AttributeAttachments = "mail.attachments"
	AttributePermanent   = "mail.permanent"
)

// Options configures the traced transport.
type Options struct {
	Transport mail.Transport // required; the transport to trace
	// TracerProvider starts the spans; required. Pass the provider main builds, or
	// otel.GetTracerProvider() when main installed it globally.
	TracerProvider trace.TracerProvider
	// Name is the mail.transport attribute, e.g. "smtp" or "resend"; empty records none.
	Name string
}

type transport struct {
	next   mail.Transport
	tracer trace.Tracer
	name   string
}

// NewTransport returns options.Transport wrapped in a span per send. Put it between the Mailer
// and the real transport: mail.New(&mail.Options{Transport: traced}). A failed send sets the
// span's status to error with the description "permanent failure" or "temporary failure" and the
// mail.permanent attribute. The error text is not recorded: a server's reply quotes addresses.
func NewTransport(options *Options) (mail.Transport, error) {
	if options == nil || options.Transport == nil {
		return nil, errors.New("oteladapter: Options.Transport is required")
	}
	if options.TracerProvider == nil {
		return nil, errors.New("oteladapter: Options.TracerProvider is required")
	}
	return &transport{next: options.Transport, tracer: options.TracerProvider.Tracer(ScopeName), name: options.Name}, nil
}

func (t *transport) Send(ctx context.Context, message *mail.Message) error {
	attributes := []attribute.KeyValue{
		attribute.Int(AttributeRecipients, len(message.To)),
		attribute.Int(AttributeAttachments, len(message.Attachments)),
	}
	if t.name != "" {
		attributes = append(attributes, attribute.String(AttributeTransport, t.name))
	}
	ctx, span := t.tracer.Start(ctx, SpanName, trace.WithSpanKind(trace.SpanKindClient), trace.WithAttributes(attributes...))
	defer span.End()
	err := t.next.Send(ctx, message)
	if err != nil {
		// Never the error text: a server's reply usually quotes the rejected address.
		permanent := errors.Is(err, mail.ErrPermanent)
		description := "temporary failure"
		if permanent {
			description = "permanent failure"
		}
		span.SetStatus(codes.Error, description)
		span.SetAttributes(attribute.Bool(AttributePermanent, permanent))
	}
	return err
}
