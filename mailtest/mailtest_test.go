package mailtest_test

import (
	"encoding/base64"
	"errors"
	"net"
	"net/textproto"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"
	"go.uber.org/goleak"

	"github.com/govengers/mail"
	"github.com/govengers/mail/mailtest"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

func TestRecorderKeepsCopiesAndFailsOnRequest(t *testing.T) {
	recorder := &mailtest.Recorder{}
	message := &mail.Message{To: []string{"a@example.com"}, Subject: "One", HTML: `<a href="https://example.com/confirm?t=1&amp;x=2">c</a>`}

	require.NoError(t, recorder.Send(t.Context(), message))
	message.To[0] = "changed@example.com"
	recorder.FailWith(errors.New("down"))
	require.ErrorContains(t, recorder.Send(t.Context(), message), "down")
	recorder.FailWith(nil)

	messages := recorder.Messages()
	require.Len(t, messages, 1)
	require.Equal(t, []string{"a@example.com"}, messages[0].To)
	require.Equal(t, []string{"https://example.com/confirm?t=1&x=2"}, mailtest.Links(messages[0]))
	recorder.Reset()
	require.Empty(t, recorder.Messages())
}

func TestParseReadsASinglePartMessage(t *testing.T) {
	raw := "From: =?utf-8?q?J=C3=B6rg?= <j@example.com>\r\nTo: a@example.com\r\nSubject: =?utf-8?q?Gr=C3=BC=C3=9Fe?=\r\n" +
		"X-Custom: v\r\nContent-Type: text/plain; charset=utf-8\r\nContent-Transfer-Encoding: base64\r\n\r\n" +
		base64.StdEncoding.EncodeToString([]byte("hallo\r\nwelt\r\n")) + "\r\n"

	message, err := mailtest.Parse([]byte(raw))

	require.NoError(t, err)
	require.Equal(t, "Jörg <j@example.com>", message.From)
	require.Equal(t, "Grüße", message.Subject)
	require.Equal(t, "hallo\nwelt\n", message.Text)
	require.Equal(t, map[string]string{"X-Custom": "v"}, message.Headers)
}

func TestParseRejectsBrokenMessages(t *testing.T) {
	for name, raw := range map[string]string{
		"no header end":   "From",
		"bad from":        "From: nope\r\nTo: a@example.com\r\nContent-Type: text/plain\r\n\r\nx",
		"bad to":          "From: a@example.com\r\nTo: nope\r\nContent-Type: text/plain\r\n\r\nx",
		"bad reply-to":    "From: a@example.com\r\nTo: a@example.com\r\nReply-To: nope\r\nContent-Type: text/plain\r\n\r\nx",
		"bad subject":     "From: a@example.com\r\nTo: a@example.com\r\nSubject: =?bogus?q?x?=\r\nContent-Type: text/plain\r\n\r\nx",
		"bad type":        "From: a@example.com\r\nTo: a@example.com\r\nContent-Type: /\r\n\r\nx",
		"unknown part":    "From: a@example.com\r\nTo: a@example.com\r\nContent-Type: image/png\r\n\r\nx",
		"broken boundary": "From: a@example.com\r\nTo: a@example.com\r\nContent-Type: multipart/mixed; boundary=b\r\n\r\n--b\r\nbroken",
	} {
		_, err := mailtest.Parse([]byte(raw))
		require.Error(t, err, name)
	}
}

func TestTheFakeSMTPServerSpeaksTheProtocol(t *testing.T) {
	server := mailtest.NewSMTPServer(t, &mailtest.SMTPServerOptions{Username: "u", Password: "p"})
	conn, err := textproto.Dial("tcp", net.JoinHostPort(server.Host(), strconv.Itoa(server.Port())))
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	expect := func(command string, code int) {
		t.Helper()
		if command != "" {
			require.NoError(t, conn.PrintfLine("%s", command))
		}
		_, _, err := conn.ReadResponse(code)
		require.NoError(t, err, command)
	}

	expect("", 220)
	expect("HELO client", 250)
	expect("NOOP", 250)
	expect("STARTTLS", 502)
	expect("AUTH LOGIN", 504)
	expect("MAIL FROM:<a@example.com>", 530)
	expect("AUTH PLAIN", 334)
	expect(base64.StdEncoding.EncodeToString([]byte("\x00u\x00wrong")), 535)
	expect("AUTH PLAIN "+base64.StdEncoding.EncodeToString([]byte("\x00u\x00p")), 235)
	expect("RCPT TO:<b@example.com>", 503)
	expect("DATA", 503)
	expect("MAIL TO:<a@example.com>", 501)
	expect("MAIL FROM:<a@example.com>", 250)
	expect("RCPT FROM:<b@example.com>", 501)
	expect("RSET", 250)
	expect("MAIL FROM:<a@example.com>", 250)
	expect("RCPT TO:<b@example.com>", 250)
	expect("DATA", 354)
	expect("From: a@example.com\r\nTo: b@example.com\r\nSubject: s\r\nContent-Type: text/plain\r\n\r\n..dot\r\n.", 250)
	expect("VRFY b", 502)
	expect("QUIT", 221)

	messages := server.Messages()
	require.Len(t, messages, 1)
	require.Equal(t, ".dot\n", messages[0].Message.Text)
}
