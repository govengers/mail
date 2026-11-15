package smtp_test

import (
	"context"
	"crypto/tls"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.uber.org/goleak"

	"github.com/govengers/mail"
	"github.com/govengers/mail/mailtest"
	"github.com/govengers/mail/smtp"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

func fullMessage() *mail.Message {
	return &mail.Message{
		From:    "Tasks Bot <noreply@example.com>",
		To:      []string{"Ada Lovelace <ada@example.com>", "grace@example.com"},
		ReplyTo: "Support <help@example.com>",
		Subject: "Grüße zum Start, das ist ein sehr langer Betreff, der über mehrere Zeilen gefaltet werden muss",
		Text:    "Hallo Ada,\n.a line starting with a dot\n" + strings.Repeat("long ", 40) + "\n",
		HTML:    `<p style="color:#333">Hallo Ada, <a href="https://example.com/x?a=1&b=2">x</a></p>` + "\n",
		Headers: map[string]string{"List-Unsubscribe": "<https://example.com/u>", "X-Note": "Größe"},
		Attachments: []mail.Attachment{
			{Filename: "bericht März.pdf", ContentType: "application/pdf", Content: []byte(strings.Repeat("%PDF-1.7 ", 30))},
			{Filename: "data.bin", Content: []byte{0, 1, 2, 255}},
		},
	}
}

func newTransport(t *testing.T, options *smtp.Options) *smtp.Transport {
	t.Helper()
	transport, err := smtp.New(options)
	require.NoError(t, err)
	return transport
}

func TestSendWithSTARTTLSAndAuthDeliversEveryPart(t *testing.T) {
	serverTLS, clientTLS := mailtest.TLSConfigs(t)
	server := mailtest.NewSMTPServer(t, &mailtest.SMTPServerOptions{TLSConfig: serverTLS, Username: "user", Password: "secret"})
	transport := newTransport(t, &smtp.Options{
		Host: server.Host(), Port: server.Port(), Username: "user", Password: "secret", TLSConfig: clientTLS, LocalName: "client.example.com",
	})
	message := fullMessage()

	require.NoError(t, transport.Send(t.Context(), message))

	received := server.Messages()
	require.Len(t, received, 1)
	require.Equal(t, "noreply@example.com", received[0].From)
	require.Equal(t, []string{"ada@example.com", "grace@example.com"}, received[0].To)
	parsed := received[0].Message
	require.Equal(t, message.Subject, parsed.Subject)
	require.Equal(t, message.From, parsed.From)
	require.Equal(t, message.To, parsed.To)
	require.Equal(t, message.ReplyTo, parsed.ReplyTo)
	require.Equal(t, message.Text, parsed.Text)
	require.Equal(t, message.HTML, parsed.HTML)
	require.Equal(t, "<https://example.com/u>", parsed.Headers["List-Unsubscribe"])
	require.Equal(t, "Größe", parsed.Headers["X-Note"])
	require.Contains(t, string(received[0].Data), "\nMessage-ID: <")
	require.Contains(t, string(received[0].Data), "@example.com>\n")
	require.Len(t, parsed.Attachments, 2)
	require.Equal(t, message.Attachments[0].Filename, parsed.Attachments[0].Filename)
	require.Equal(t, message.Attachments[0].Content, parsed.Attachments[0].Content)
	require.Equal(t, "application/octet-stream", parsed.Attachments[1].ContentType)
	require.Equal(t, message.Attachments[1].Content, parsed.Attachments[1].Content)
	for _, line := range strings.Split(string(received[0].Data), "\n") {
		require.LessOrEqual(t, len(line), 998, "RFC 5322 line limit")
	}
}

func TestSendWithImplicitTLS(t *testing.T) {
	serverTLS, clientTLS := mailtest.TLSConfigs(t)
	server := mailtest.NewSMTPServer(t, &mailtest.SMTPServerOptions{TLSConfig: serverTLS, ImplicitTLS: true, Username: "u", Password: "p"})
	transport := newTransport(t, &smtp.Options{Host: server.Host(), Port: server.Port(), Security: smtp.SecurityTLS, TLSConfig: clientTLS, Username: "u", Password: "p"})

	message := &mail.Message{From: "a@example.com", To: []string{"b@example.com"}, Subject: "Hi", Text: "only text\n"}
	require.NoError(t, transport.Send(t.Context(), message))

	parsed := server.Messages()[0].Message
	require.Equal(t, "only text\n", parsed.Text)
	require.Empty(t, parsed.HTML)
}

func TestSendWithoutEncryptionToAPlainServer(t *testing.T) {
	server := mailtest.NewSMTPServer(t, nil)
	transport := newTransport(t, &smtp.Options{Host: server.Host(), Port: server.Port(), Security: smtp.SecurityNone})

	message := &mail.Message{From: "a@example.com", To: []string{"b@example.com"}, Subject: "Hi", HTML: "<p>only html</p>"}
	require.NoError(t, transport.Send(t.Context(), message))
	require.Equal(t, "<p>only html</p>\n", server.Messages()[0].Message.HTML, "the message ends with a line break")
}

func TestSendRefusesAServerWithoutSTARTTLS(t *testing.T) {
	server := mailtest.NewSMTPServer(t, nil)
	transport := newTransport(t, &smtp.Options{Host: server.Host(), Port: server.Port()})

	err := transport.Send(t.Context(), &mail.Message{From: "a@example.com", To: []string{"b@example.com"}, Subject: "Hi", Text: "x"})

	require.ErrorContains(t, err, "STARTTLS")
	require.NotErrorIs(t, err, mail.ErrPermanent)
	require.Empty(t, server.Messages())
}

func TestSendRefusesAnUntrustedCertificate(t *testing.T) {
	serverTLS, _ := mailtest.TLSConfigs(t)
	server := mailtest.NewSMTPServer(t, &mailtest.SMTPServerOptions{TLSConfig: serverTLS})
	transport := newTransport(t, &smtp.Options{Host: server.Host(), Port: server.Port(), TLSConfig: &tls.Config{MinVersion: tls.VersionTLS12}})

	err := transport.Send(t.Context(), &mail.Message{From: "a@example.com", To: []string{"b@example.com"}, Subject: "Hi", Text: "x"})

	require.Error(t, err)
	require.Empty(t, server.Messages())
}

func TestSendClassifiesReplies(t *testing.T) {
	for name, testCase := range map[string]struct {
		command, reply string
		permanent      bool
	}{
		"unknown recipient": {command: "RCPT TO:<b@example.com>", reply: "550 5.1.1 no such user", permanent: true},
		"greylisted":        {command: "RCPT TO:<b@example.com>", reply: "451 4.7.1 try again later"},
		"rejected content":  {command: ".", reply: "554 5.7.1 spam", permanent: true},
		"mailbox busy":      {command: "MAIL FROM:<a@example.com>", reply: "421 4.3.2 busy"},
	} {
		server := mailtest.NewSMTPServer(t, &mailtest.SMTPServerOptions{Reply: func(command string) string {
			if strings.HasPrefix(command, testCase.command) {
				return testCase.reply
			}
			return ""
		}})
		transport := newTransport(t, &smtp.Options{Host: server.Host(), Port: server.Port(), Security: smtp.SecurityNone})

		err := transport.Send(t.Context(), &mail.Message{From: "a@example.com", To: []string{"b@example.com"}, Subject: "Hi", Text: "x"})

		require.Error(t, err, name)
		require.Equal(t, testCase.permanent, errors.Is(err, mail.ErrPermanent), name)
		require.Empty(t, server.Messages(), name)
	}
}

func TestSendFailsOnWrongCredentials(t *testing.T) {
	serverTLS, clientTLS := mailtest.TLSConfigs(t)
	server := mailtest.NewSMTPServer(t, &mailtest.SMTPServerOptions{TLSConfig: serverTLS, Username: "user", Password: "secret"})
	transport := newTransport(t, &smtp.Options{Host: server.Host(), Port: server.Port(), Username: "user", Password: "wrong", TLSConfig: clientTLS})

	err := transport.Send(t.Context(), &mail.Message{From: "a@example.com", To: []string{"b@example.com"}, Subject: "Hi", Text: "x"})

	require.ErrorIs(t, err, mail.ErrPermanent)
}

func TestSendReturnsTheContextsErrorBeforeConnecting(t *testing.T) {
	server := mailtest.NewSMTPServer(t, nil)
	transport := newTransport(t, &smtp.Options{Host: server.Host(), Port: server.Port(), Security: smtp.SecurityNone})
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	err := transport.Send(ctx, &mail.Message{From: "a@example.com", To: []string{"b@example.com"}, Subject: "Hi", Text: "x"})

	require.ErrorIs(t, err, context.Canceled)
}

func TestCancellingTheContextInterruptsAWaitingSend(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	release := make(chan struct{})
	server := mailtest.NewSMTPServer(t, &mailtest.SMTPServerOptions{Reply: func(command string) string {
		if strings.HasPrefix(command, "MAIL") {
			cancel()  // the client now waits for a reply that only comes after the test
			<-release // closed by the cleanup below, which runs before the server's
		}
		return ""
	}})
	t.Cleanup(func() { close(release) })
	transport := newTransport(t, &smtp.Options{Host: server.Host(), Port: server.Port(), Security: smtp.SecurityNone})

	err := transport.Send(ctx, &mail.Message{From: "a@example.com", To: []string{"b@example.com"}, Subject: "Hi", Text: "x"})

	require.ErrorIs(t, err, context.Canceled)
	require.NotErrorIs(t, err, mail.ErrPermanent)
}

func TestNewValidatesItsOptions(t *testing.T) {
	for name, options := range map[string]*smtp.Options{
		"nil":              nil,
		"no host":          {},
		"unknown security": {Host: "mail.example.com", Security: "ssl"},
		"negative timeout": {Host: "mail.example.com", Timeout: -time.Second},
		"plain auth":       {Host: "mail.example.com", Security: smtp.SecurityNone, Username: "u"},
	} {
		_, err := smtp.New(options)
		require.Error(t, err, name)
	}
	_, err := smtp.New(&smtp.Options{Host: "localhost", Security: smtp.SecurityNone, Username: "u"})
	require.NoError(t, err)
}
