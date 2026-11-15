package oteladapter_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"

	"github.com/govengers/mail"
	"github.com/govengers/mail/mailtest"
	"github.com/govengers/mail/oteladapter"
)

func newTraced(t *testing.T, next mail.Transport) (mail.Transport, *tracetest.SpanRecorder) {
	t.Helper()
	spans := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(spans))
	t.Cleanup(func() { _ = provider.Shutdown(t.Context()) })
	traced, err := oteladapter.NewTransport(&oteladapter.Options{Transport: next, TracerProvider: provider, Name: "smtp"})
	require.NoError(t, err)
	return traced, spans
}

func TestASendIsAClientSpan(t *testing.T) {
	traced, spans := newTraced(t, &mailtest.Recorder{})
	message := &mail.Message{To: []string{"a@example.com", "b@example.com"}, Attachments: []mail.Attachment{{Filename: "a"}}}

	require.NoError(t, traced.Send(t.Context(), message))

	ended := spans.Ended()
	require.Len(t, ended, 1)
	require.Equal(t, oteladapter.SpanName, ended[0].Name())
	require.Equal(t, trace.SpanKindClient, ended[0].SpanKind())
	require.Equal(t, codes.Unset, ended[0].Status().Code)
	require.ElementsMatch(t, []attribute.KeyValue{
		attribute.Int(oteladapter.AttributeRecipients, 2),
		attribute.Int(oteladapter.AttributeAttachments, 1),
		attribute.String(oteladapter.AttributeTransport, "smtp"),
	}, ended[0].Attributes())
}

func TestAFailedSendIsAnError(t *testing.T) {
	for _, testCase := range []struct {
		err         error
		permanent   bool
		description string
	}{
		{err: errors.New("451 4.3.0 <ada@example.com> try again"), description: "temporary failure"},
		{err: fmt.Errorf("%w: 550 5.1.1 <ada@example.com>: Recipient address rejected", mail.ErrPermanent), permanent: true, description: "permanent failure"},
	} {
		recorder := &mailtest.Recorder{}
		recorder.FailWith(testCase.err)
		traced, spans := newTraced(t, recorder)

		require.ErrorIs(t, traced.Send(t.Context(), &mail.Message{To: []string{"a@example.com"}}), testCase.err)

		span := spans.Ended()[0]
		require.Equal(t, codes.Error, span.Status().Code)
		require.Equal(t, testCase.description, span.Status().Description)
		require.Contains(t, span.Attributes(), attribute.Bool(oteladapter.AttributePermanent, testCase.permanent))
		require.Empty(t, span.Events(), "the error text quotes the address")
	}
}

func TestNewTransportValidatesItsOptions(t *testing.T) {
	_, err := oteladapter.NewTransport(nil)
	require.Error(t, err)
	_, err = oteladapter.NewTransport(&oteladapter.Options{Transport: &mailtest.Recorder{}})
	require.Error(t, err)
}
