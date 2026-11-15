package mailtest_test

import (
	"encoding/base64"
	"errors"
	"net"
	"net/http"
	"net/textproto"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"go.uber.org/goleak"

	"github.com/govengers/mail"
	"github.com/govengers/mail/mailtest"
	"github.com/govengers/mail/smtp"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

func TestRecorderKeepsCopiesAndFailsOnRequest(t *testing.T) {
	recorder := &mailtest.Recorder{}
	message := &mail.Message{
		To: []string{"a@example.com"}, Subject: "One", HTML: `<a href="https://example.com/confirm?t=1&amp;x=2">c</a>`,
		Headers: map[string]string{"X-Tag": "a"}, Attachments: []mail.Attachment{{Filename: "a", Content: []byte("x")}},
	}

	require.NoError(t, recorder.Send(t.Context(), message))
	message.To[0] = "changed@example.com"
	message.Headers["X-Tag"] = "changed"
	message.Attachments[0].Content[0] = 'y'
	recorder.FailWith(errors.New("down"))
	require.ErrorContains(t, recorder.Send(t.Context(), message), "down")
	recorder.FailWith(nil)

	messages := recorder.Messages()
	require.Len(t, messages, 1)
	require.Equal(t, []string{"a@example.com"}, messages[0].To)
	require.Equal(t, "a", messages[0].Headers["X-Tag"])
	require.Equal(t, []byte("x"), messages[0].Attachments[0].Content)
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

func TestTheFakeSMTPServerUpgradesToTLSAndParsesMultipartMail(t *testing.T) {
	serverTLS, clientTLS := mailtest.TLSConfigs(t)
	server := mailtest.NewSMTPServer(t, &mailtest.SMTPServerOptions{TLSConfig: serverTLS})
	transport, err := smtp.New(&smtp.Options{Host: server.Host(), Port: server.Port(), TLSConfig: clientTLS})
	require.NoError(t, err)
	message := &mail.Message{
		From: "a@example.com", To: []string{"b@example.com"}, Subject: "Report", Text: "see attached\n", HTML: "<p>see attached</p>\n",
		Attachments: []mail.Attachment{{Filename: "r.csv", ContentType: "text/csv", Content: []byte("a,b\n1,2\n")}},
	}

	require.NoError(t, transport.Send(t.Context(), message))

	parsed := server.Messages()[0].Message
	require.Equal(t, message.Text, parsed.Text)
	require.Equal(t, message.HTML, parsed.HTML)
	require.Equal(t, message.Attachments, parsed.Attachments)
}

func TestTheFakeResendAPIRejectsWhatResendWould(t *testing.T) {
	server := mailtest.NewResendServer(t, &mailtest.ResendServerOptions{Reply: func(request *mailtest.ResendRequest) (int, string) {
		if request.Message.Subject == "throttled" {
			return http.StatusTooManyRequests, "rate_limit_exceeded"
		}
		return 0, ""
	}})
	post := func(method, path, body string) int {
		t.Helper()
		request, err := http.NewRequestWithContext(t.Context(), method, server.URL()+path, strings.NewReader(body))
		require.NoError(t, err)
		response, err := http.DefaultClient.Do(request)
		require.NoError(t, err)
		require.NoError(t, response.Body.Close())
		return response.StatusCode
	}

	require.Equal(t, http.StatusNotFound, post(http.MethodGet, "/emails", ""))
	require.Equal(t, http.StatusNotFound, post(http.MethodPost, "/domains", ""))
	require.Equal(t, http.StatusUnprocessableEntity, post(http.MethodPost, "/emails", "not json"))
	require.Equal(t, http.StatusTooManyRequests, post(http.MethodPost, "/emails", `{"subject":"throttled"}`))
	require.Equal(t, http.StatusOK, post(http.MethodPost, "/emails", `{"subject":"fine","reply_to":["r@example.com"],"attachments":[{"filename":"a","content":"aGk="}]}`))

	require.Len(t, server.Requests(), 2)
	delivered := server.Messages()
	require.Len(t, delivered, 1)
	require.Equal(t, "r@example.com", delivered[0].ReplyTo)
	require.Equal(t, []byte("hi"), delivered[0].Attachments[0].Content)
	http.DefaultClient.CloseIdleConnections()
}
