package mail_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
	"go.uber.org/goleak"

	"github.com/govengers/mail"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

// capture is a Transport that records what it receives and returns err.
type capture struct {
	mutex    sync.Mutex
	messages []*mail.Message
	err      error
}

func (c *capture) Send(_ context.Context, message *mail.Message) error {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	c.messages = append(c.messages, message)
	return c.err
}

// outcomes is an Observer that records every outcome.
type outcomes struct {
	mutex sync.Mutex
	all   []*mail.Outcome
}

func (o *outcomes) SendFinished(outcome *mail.Outcome) {
	o.mutex.Lock()
	defer o.mutex.Unlock()
	o.all = append(o.all, outcome)
}

func validMessage() *mail.Message {
	return &mail.Message{To: []string{"Ada <ada@example.com>"}, Subject: "Hello", Text: "Hi\n"}
}

func TestMailerFillsInTheDefaultSenderWithoutChangingTheCallersMessage(t *testing.T) {
	transport := &capture{}
	observer := &outcomes{}
	mailer, err := mail.New(&mail.Options{Transport: transport, From: "Tasks <noreply@example.com>", Observer: observer})
	require.NoError(t, err)
	message := validMessage()

	require.NoError(t, mailer.Send(t.Context(), message))

	require.Empty(t, message.From)
	require.Len(t, transport.messages, 1)
	require.Equal(t, "Tasks <noreply@example.com>", transport.messages[0].From)
	require.Len(t, observer.all, 1)
	require.Equal(t, mail.ResultSent, observer.all[0].Result)
	require.NoError(t, observer.all[0].Err)
	require.Equal(t, "Tasks <noreply@example.com>", observer.all[0].Message.From)
}

func TestMailerKeepsAMessagesOwnSender(t *testing.T) {
	transport := &capture{}
	mailer, err := mail.New(&mail.Options{Transport: transport, From: "noreply@example.com"})
	require.NoError(t, err)
	message := validMessage()
	message.From = "billing@example.com"

	require.NoError(t, mailer.Send(t.Context(), message))
	require.Equal(t, "billing@example.com", transport.messages[0].From)
}

func TestMailerReportsHowASendEnded(t *testing.T) {
	for name, testCase := range map[string]struct {
		transportError error
		message        *mail.Message
		result         mail.Result
	}{
		"invalid":   {message: &mail.Message{Subject: "x", Text: "x"}, result: mail.ResultInvalid},
		"nil":       {message: nil, result: mail.ResultInvalid},
		"rejected":  {transportError: fmt.Errorf("%w: 550 no such user", mail.ErrPermanent), message: validMessage(), result: mail.ResultRejected},
		"temporary": {transportError: errors.New("connection reset"), message: validMessage(), result: mail.ResultFailed},
	} {
		transport := &capture{err: testCase.transportError}
		observer := &outcomes{}
		mailer, err := mail.New(&mail.Options{Transport: transport, From: "noreply@example.com", Observer: observer})
		require.NoError(t, err)

		err = mailer.Send(t.Context(), testCase.message)

		require.Error(t, err, name)
		if testCase.message == nil {
			require.Empty(t, observer.all, name)
			continue
		}
		require.Len(t, observer.all, 1, name)
		require.Equal(t, testCase.result, observer.all[0].Result, name)
		require.ErrorIs(t, observer.all[0].Err, err, name)
	}
}

func TestMailerDoesNotSendAnInvalidMessage(t *testing.T) {
	transport := &capture{}
	mailer, err := mail.New(&mail.Options{Transport: transport})
	require.NoError(t, err)

	err = mailer.Send(t.Context(), validMessage())

	require.ErrorIs(t, err, mail.ErrInvalidMessage)
	require.ErrorIs(t, err, mail.ErrPermanent)
	require.Empty(t, transport.messages, "no From and no default")
}

func TestNewValidatesItsOptions(t *testing.T) {
	_, err := mail.New(nil)
	require.Error(t, err)
	_, err = mail.New(&mail.Options{})
	require.Error(t, err)
	_, err = mail.New(&mail.Options{Transport: &capture{}, From: "not an address"})
	require.Error(t, err)
}

func TestValidate(t *testing.T) {
	for name, change := range map[string]func(message *mail.Message){
		"bad from":              func(message *mail.Message) { message.From = "nope" },
		"no recipient":          func(message *mail.Message) { message.To = nil },
		"bad recipient":         func(message *mail.Message) { message.To = []string{"a@example.com", "b"} },
		"bad reply-to":          func(message *mail.Message) { message.ReplyTo = "x" },
		"empty subject":         func(message *mail.Message) { message.Subject = "" },
		"subject line break":    func(message *mail.Message) { message.Subject = "a\r\nBcc: x@example.com" },
		"no body":               func(message *mail.Message) { message.Text = "" },
		"long idempotency key":  func(message *mail.Message) { message.IdempotencyKey = strings.Repeat("k", 257) },
		"reserved header":       func(message *mail.Message) { message.Headers = map[string]string{"bcc": "x@example.com"} },
		"header name":           func(message *mail.Message) { message.Headers = map[string]string{"X Bad": "v"} },
		"header value":          func(message *mail.Message) { message.Headers = map[string]string{"X-Tag": "a\nb"} },
		"attachment path":       func(message *mail.Message) { message.Attachments = []mail.Attachment{{Filename: "../a.txt"}} },
		"attachment no name":    func(message *mail.Message) { message.Attachments = []mail.Attachment{{}} },
		"attachment media type": func(message *mail.Message) { message.Attachments = []mail.Attachment{{Filename: "a", ContentType: "text/"}} },
	} {
		message := validMessage()
		message.From = "noreply@example.com"
		change(message)
		err := message.Validate()
		require.ErrorIs(t, err, mail.ErrInvalidMessage, name)
		require.ErrorIs(t, err, mail.ErrPermanent, name)
	}

	message := validMessage()
	message.From = "noreply@example.com"
	message.ReplyTo = "help@example.com"
	message.HTML, message.Text = "<p>x</p>", ""
	message.Headers = map[string]string{"List-Unsubscribe": "<https://example.com/u>"}
	message.Attachments = []mail.Attachment{{Filename: "report.pdf", ContentType: "application/pdf", Content: []byte("%PDF")}}
	message.IdempotencyKey = strings.Repeat("k", 256)
	require.NoError(t, message.Validate())
}
