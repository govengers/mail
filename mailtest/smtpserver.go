package mailtest

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"net"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/govengers/mail"
)

// SMTPServerOptions configures a fake SMTP server.
type SMTPServerOptions struct {
	// TLSConfig, with a certificate, makes the server offer STARTTLS, or speak TLS from the first
	// byte with ImplicitTLS. Without it the server is plain and offers no STARTTLS.
	TLSConfig   *tls.Config
	ImplicitTLS bool
	// Username and Password, when set, make AUTH PLAIN with them required before MAIL.
	Username string
	Password string
	// Reply, when set, sees every command line and "." after a message's data, and returns a reply
	// line ("550 5.1.1 no such user") to send instead of handling the command, or "" to handle it.
	Reply func(command string) string
}

// SMTPMessage is a message the fake SMTP server accepted.
type SMTPMessage struct {
	From    string   // the envelope sender, MAIL FROM
	To      []string // the envelope recipients, RCPT TO
	Data    []byte   // the message as sent, dot-unstuffed, with LF line endings
	Message *mail.Message
}

// SMTPServer is a fake SMTP server on a loopback port. It speaks enough of RFC 5321 for net/smtp
// and most clients: EHLO, HELO, STARTTLS, AUTH PLAIN, MAIL, RCPT, DATA, RSET, NOOP, QUIT.
type SMTPServer struct {
	tb        testing.TB
	listener  net.Listener
	options   SMTPServerOptions
	waitGroup sync.WaitGroup
	mutex     sync.Mutex
	messages  []*SMTPMessage
	conns     map[net.Conn]bool
}

// NewSMTPServer starts a fake SMTP server; tb's cleanup stops it and closes open connections. A
// nil options value is a plain server without authentication.
func NewSMTPServer(tb testing.TB, options *SMTPServerOptions) *SMTPServer {
	tb.Helper()
	server := &SMTPServer{tb: tb, conns: map[net.Conn]bool{}}
	if options != nil {
		server.options = *options
	}
	listener, err := (&net.ListenConfig{}).Listen(tb.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		tb.Fatalf("mailtest: listen: %v", err)
	}
	if server.options.ImplicitTLS {
		if server.options.TLSConfig == nil {
			tb.Fatal("mailtest: ImplicitTLS needs a TLSConfig")
		}
		listener = tls.NewListener(listener, server.options.TLSConfig)
	}
	server.listener = listener
	server.waitGroup.Go(server.accept)
	tb.Cleanup(server.close)
	return server
}

// Host is the server's host, 127.0.0.1.
func (s *SMTPServer) Host() string {
	return s.listener.Addr().(*net.TCPAddr).IP.String()
}

// Port is the server's port.
func (s *SMTPServer) Port() int {
	return s.listener.Addr().(*net.TCPAddr).Port
}

// Messages returns the accepted messages in order.
func (s *SMTPServer) Messages() []*SMTPMessage {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	return append([]*SMTPMessage(nil), s.messages...)
}

func (s *SMTPServer) close() {
	_ = s.listener.Close()
	s.mutex.Lock()
	for conn := range s.conns {
		_ = conn.Close()
	}
	s.mutex.Unlock()
	s.waitGroup.Wait()
}

func (s *SMTPServer) accept() {
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			return
		}
		s.mutex.Lock()
		s.conns[conn] = true
		s.mutex.Unlock()
		s.waitGroup.Go(func() {
			defer s.forget(conn)
			(&smtpSession{server: s, conn: conn, text: textproto.NewConn(conn)}).run()
		})
	}
}

func (s *SMTPServer) forget(conn net.Conn) {
	_ = conn.Close()
	s.mutex.Lock()
	delete(s.conns, conn)
	s.mutex.Unlock()
}

func (s *SMTPServer) store(message *SMTPMessage) {
	parsed, err := Parse(message.Data)
	if err != nil {
		s.tb.Errorf("mailtest: the fake SMTP server received a message that does not parse: %v", err)
	}
	message.Message = parsed
	s.mutex.Lock()
	defer s.mutex.Unlock()
	s.messages = append(s.messages, message)
}

// smtpSession is one client connection.
type smtpSession struct {
	server        *SMTPServer
	conn          net.Conn
	text          *textproto.Conn
	tls           bool
	authenticated bool
	envelope      *SMTPMessage
}

func (s *smtpSession) run() {
	_, s.tls = s.conn.(*tls.Conn)
	if s.reply("220 mailtest ESMTP") != nil {
		return
	}
	for {
		line, err := s.text.ReadLine()
		if err != nil {
			return
		}
		if override := s.override(line); override != "" {
			if s.reply(override) != nil {
				return
			}
			continue
		}
		if quit, err := s.handle(line); quit || err != nil {
			return
		}
	}
}

func (s *smtpSession) override(line string) string {
	if s.server.options.Reply == nil {
		return ""
	}
	return s.server.options.Reply(line)
}

func (s *smtpSession) reply(line string) error {
	return s.text.PrintfLine("%s", line)
}

// handle answers one command; it reports whether the session ends.
func (s *smtpSession) handle(line string) (bool, error) {
	verb, argument, _ := strings.Cut(line, " ")
	switch strings.ToUpper(verb) {
	case "EHLO":
		return false, s.reply(s.extensions())
	case "HELO", "NOOP":
		return false, s.reply("250 OK")
	case "STARTTLS":
		return false, s.startTLS()
	case "AUTH":
		return false, s.authenticate(argument)
	case "MAIL":
		return false, s.mail(argument)
	case "RCPT":
		return false, s.recipient(argument)
	case "DATA":
		return false, s.data()
	case "RSET":
		s.envelope = nil
		return false, s.reply("250 OK")
	case "QUIT":
		_ = s.reply("221 bye")
		return true, nil
	default:
		return false, s.reply("502 5.5.2 command not implemented")
	}
}

func (s *smtpSession) extensions() string {
	lines := []string{"250-mailtest", "250-8BITMIME"}
	if s.server.options.TLSConfig != nil && !s.tls {
		lines = append(lines, "250-STARTTLS")
	}
	if s.server.options.Username != "" {
		lines = append(lines, "250-AUTH PLAIN")
	}
	lines = append(lines, "250 SMTPUTF8")
	return strings.Join(lines, "\r\n")
}

func (s *smtpSession) startTLS() error {
	if s.server.options.TLSConfig == nil || s.tls {
		return s.reply("502 5.5.1 STARTTLS not offered")
	}
	if err := s.reply("220 2.0.0 ready to start TLS"); err != nil {
		return err
	}
	secured := tls.Server(s.conn, s.server.options.TLSConfig)
	if err := secured.HandshakeContext(context.Background()); err != nil { // closing the server ends it
		return err
	}
	s.conn, s.text, s.tls, s.envelope = secured, textproto.NewConn(secured), true, nil
	return nil
}

func (s *smtpSession) authenticate(argument string) error {
	mechanism, response, _ := strings.Cut(argument, " ")
	if !strings.EqualFold(mechanism, "PLAIN") || s.server.options.Username == "" {
		return s.reply("504 5.5.4 unrecognized authentication type")
	}
	if response == "" {
		if err := s.reply("334 "); err != nil {
			return err
		}
		line, err := s.text.ReadLine()
		if err != nil {
			return err
		}
		response = line
	}
	decoded, err := base64.StdEncoding.DecodeString(response)
	parts := bytes.Split(decoded, []byte{0})
	if err != nil || len(parts) != 3 || string(parts[1]) != s.server.options.Username || string(parts[2]) != s.server.options.Password {
		return s.reply("535 5.7.8 authentication credentials invalid")
	}
	s.authenticated = true
	return s.reply("235 2.7.0 authentication successful")
}

func (s *smtpSession) mail(argument string) error {
	if s.server.options.Username != "" && !s.authenticated {
		return s.reply("530 5.7.0 authentication required")
	}
	address, ok := pathArgument(argument, "FROM:")
	if !ok {
		return s.reply("501 5.5.4 syntax: MAIL FROM:<address>")
	}
	s.envelope = &SMTPMessage{From: address}
	return s.reply("250 2.1.0 OK")
}

func (s *smtpSession) recipient(argument string) error {
	address, ok := pathArgument(argument, "TO:")
	switch {
	case s.envelope == nil:
		return s.reply("503 5.5.1 MAIL first")
	case !ok:
		return s.reply("501 5.5.4 syntax: RCPT TO:<address>")
	}
	s.envelope.To = append(s.envelope.To, address)
	return s.reply("250 2.1.5 OK")
}

func (s *smtpSession) data() error {
	if s.envelope == nil || len(s.envelope.To) == 0 {
		return s.reply("503 5.5.1 RCPT first")
	}
	if err := s.reply("354 end data with <CR><LF>.<CR><LF>"); err != nil {
		return err
	}
	data, err := s.text.ReadDotBytes()
	if err != nil {
		return err
	}
	envelope := s.envelope
	s.envelope = nil
	if override := s.override("."); override != "" {
		return s.reply(override)
	}
	envelope.Data = data
	s.server.store(envelope)
	return s.reply("250 2.0.0 OK queued as " + strconv.Itoa(len(s.server.Messages())))
}

// pathArgument reads the address of "FROM:<address> ..." or "TO:<address>".
func pathArgument(argument, prefix string) (string, bool) {
	if len(argument) < len(prefix) || !strings.EqualFold(argument[:len(prefix)], prefix) {
		return "", false
	}
	path, _, _ := strings.Cut(strings.TrimSpace(argument[len(prefix):]), " ")
	if !strings.HasPrefix(path, "<") || !strings.HasSuffix(path, ">") {
		return "", false
	}
	return path[1 : len(path)-1], true
}

// TLSConfigs returns a server TLS configuration with a test certificate for 127.0.0.1, ::1 and
// example.com (net/http/httptest's), and a client configuration that trusts it: give the first to
// SMTPServerOptions.TLSConfig and the second to the smtp transport's Options.TLSConfig.
func TLSConfigs(tb testing.TB) (server, client *tls.Config) {
	tb.Helper()
	borrowed := httptest.NewUnstartedServer(http.NotFoundHandler())
	borrowed.StartTLS()
	defer borrowed.Close()
	server = borrowed.TLS.Clone()
	server.NextProtos = nil
	roots := x509.NewCertPool()
	roots.AddCert(borrowed.Certificate())
	return server, &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}
}
