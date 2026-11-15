// Package smtp sends mail over SMTP with net/smtp: plain, STARTTLS (the default) or implicit TLS,
// AUTH PLAIN, one connection per message.
//
// Delivery is at least once: when the connection breaks after the server accepted the message
// but before Send saw the reply, Send fails and a retry delivers the message again. SMTP has no
// idempotency key; the Message-ID is derived from Message.IdempotencyKey, so the copy carries the
// same Message-ID and receivers that deduplicate on it drop it. A retry that must not duplicate
// needs the resend transport.
package smtp

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	netmail "net/mail"
	netsmtp "net/smtp"
	"net/textproto"
	"strconv"
	"time"

	"github.com/govengers/mail"
)

// DefaultTimeout bounds one Send when its context has no earlier deadline.
const DefaultTimeout = 30 * time.Second

// Security is how the connection is protected.
type Security string

// The connection security modes.
const (
	SecurityStartTLS Security = "starttls" // plain connection upgraded with STARTTLS, which the server must offer; the default
	SecurityTLS      Security = "tls"      // TLS from the first byte (SMTPS, usually port 465)
	SecurityNone     Security = "none"     // no encryption; only for a relay on localhost or a test server
)

// Options configures a Transport.
type Options struct {
	Host string // required
	// Port defaults to 587 for STARTTLS, 465 for TLS and 25 for none.
	Port     int
	Security Security // empty means SecurityStartTLS
	// Username enables AUTH PLAIN, which net/smtp only sends over TLS or to localhost.
	Username string
	Password string
	// TLSConfig configures TLS; nil verifies the server's certificate for Host with the system roots.
	TLSConfig *tls.Config
	LocalName string        // the EHLO name; empty means "localhost"
	Timeout   time.Duration // bounds one Send; zero means DefaultTimeout
}

// Transport sends mail over SMTP; it implements mail.Transport and is safe for concurrent use.
type Transport struct {
	address   string
	host      string
	security  Security
	auth      netsmtp.Auth
	tlsConfig *tls.Config
	localName string
	timeout   time.Duration
}

// New returns a Transport. It does not connect.
func New(options *Options) (*Transport, error) {
	if options == nil || options.Host == "" {
		return nil, errors.New("smtp: Options.Host is required")
	}
	security := options.Security
	if security == "" {
		security = SecurityStartTLS
	}
	port := options.Port
	switch security {
	case SecurityStartTLS:
		port = orDefault(port, 587)
	case SecurityTLS:
		port = orDefault(port, 465)
	case SecurityNone:
		port = orDefault(port, 25)
	default:
		return nil, fmt.Errorf("smtp: unknown Options.Security %q", options.Security)
	}
	if options.Timeout < 0 {
		return nil, errors.New("smtp: Options.Timeout must not be negative")
	}
	transport := &Transport{
		address:   net.JoinHostPort(options.Host, strconv.Itoa(port)),
		host:      options.Host,
		security:  security,
		localName: orDefault(options.LocalName, "localhost"),
		timeout:   orDefault(options.Timeout, DefaultTimeout),
		tlsConfig: &tls.Config{ServerName: options.Host, MinVersion: tls.VersionTLS12},
	}
	if options.TLSConfig != nil {
		transport.tlsConfig = options.TLSConfig.Clone()
		if transport.tlsConfig.ServerName == "" {
			transport.tlsConfig.ServerName = options.Host
		}
	}
	if options.Username != "" && security == SecurityNone && !isLocalhost(options.Host) {
		return nil, errors.New("smtp: Options.Username needs TLS or STARTTLS unless the server is on localhost")
	}
	if options.Username != "" {
		transport.auth = netsmtp.PlainAuth("", options.Username, options.Password, options.Host)
	}
	return transport, nil
}

// isLocalhost is net/smtp's rule for sending AUTH PLAIN without TLS.
func isLocalhost(host string) bool {
	return host == "localhost" || host == "127.0.0.1" || host == "::1"
}

func orDefault[T comparable](value, fallback T) T {
	var zero T
	if value == zero {
		return fallback
	}
	return value
}

// Send delivers the message on a new connection. A 5xx reply to MAIL, RCPT or DATA (a refused
// sender, recipient or content) matches mail.ErrPermanent, except 530 (authentication required).
// Every failure before that is temporary, 5xx included: a refused greeting, STARTTLS or AUTH (a
// rotated password) is the transport's configuration, and the message goes out once it is fixed. 4xx replies, network errors and a
// cancelled context are temporary too.
func (t *Transport) Send(ctx context.Context, message *mail.Message) error {
	// The Mailer validated already; a transport used on its own (a jobs handler) must not put an
	// unchecked address into the envelope either.
	if err := message.Validate(); err != nil {
		return fmt.Errorf("smtp: %w", err)
	}
	data, err := compose(message, time.Now())
	if err != nil {
		return fmt.Errorf("%w: %w: smtp: %v", mail.ErrPermanent, mail.ErrInvalidMessage, err) //nolint:errorlint // LIB-010
	}
	ctx, cancel := context.WithTimeout(ctx, t.timeout)
	defer cancel()
	client, stop, err := t.connect(ctx)
	if err != nil {
		return t.failure(ctx, "connect", err, false)
	}
	defer func() { _ = client.Close() }()
	defer stop()
	if err := t.deliver(client, message, data); err != nil {
		return t.failure(ctx, "send", err, true)
	}
	_ = client.Quit() // the server accepted the message; a failed QUIT changes nothing
	return nil
}

// connect dials, greets and secures the connection, and authenticates. A cancelled context
// interrupts every network operation through the connection's deadline until stop is called.
func (t *Transport) connect(ctx context.Context) (client *netsmtp.Client, stop func() bool, err error) {
	var conn net.Conn
	if t.security == SecurityTLS {
		dialer := &tls.Dialer{Config: t.tlsConfig}
		conn, err = dialer.DialContext(ctx, "tcp", t.address)
	} else {
		dialer := &net.Dialer{}
		conn, err = dialer.DialContext(ctx, "tcp", t.address)
	}
	if err != nil {
		return nil, nil, err
	}
	deadline, _ := ctx.Deadline()
	_ = conn.SetDeadline(deadline)
	stop = context.AfterFunc(ctx, func() { _ = conn.SetDeadline(time.Unix(1, 0)) })
	client, err = netsmtp.NewClient(conn, t.host)
	if err != nil {
		stop()
		_ = conn.Close()
		return nil, nil, err
	}
	if err := t.handshake(client); err != nil {
		stop()
		_ = client.Close()
		return nil, nil, err
	}
	return client, stop, nil
}

func (t *Transport) handshake(client *netsmtp.Client) error {
	// net/smtp greets lazily, at MAIL without STARTTLS and AUTH; greet here so a refused greeting
	// is a connection failure, not a refused message.
	if err := client.Hello(t.localName); err != nil {
		return err
	}
	if t.security == SecurityStartTLS {
		if ok, _ := client.Extension("STARTTLS"); !ok {
			return errors.New("the server does not offer STARTTLS")
		}
		if err := client.StartTLS(t.tlsConfig); err != nil {
			return err
		}
	}
	if t.auth != nil {
		return client.Auth(t.auth)
	}
	return nil
}

func (t *Transport) deliver(client *netsmtp.Client, message *mail.Message, data []byte) error {
	from, err := netmail.ParseAddress(message.From)
	if err != nil {
		return err
	}
	if err := client.Mail(from.Address); err != nil {
		return err
	}
	for _, recipient := range message.To {
		address, err := netmail.ParseAddress(recipient)
		if err != nil {
			return err
		}
		if err := client.Rcpt(address.Address); err != nil {
			return err
		}
	}
	writer, err := client.Data()
	if err != nil {
		return err
	}
	if _, err := writer.Write(data); err != nil {
		_ = writer.Close()
		return err
	}
	return writer.Close()
}

// failure classifies err: a 5xx reply is permanent when the step's replies are about the message,
// a cancelled or expired context is returned as its cause so callers can match context.Canceled
// and context.DeadlineExceeded.
func (t *Transport) failure(ctx context.Context, step string, err error, messageStep bool) error {
	if ctxErr := context.Cause(ctx); ctxErr != nil {
		return fmt.Errorf("smtp %s %s: %w (%v)", step, t.address, ctxErr, err) //nolint:errorlint // LIB-010: only the context error is API
	}
	var reply *textproto.Error
	// 530 (authentication required) at MAIL or RCPT is the transport's configuration, not the message.
	if messageStep && errors.As(err, &reply) && reply.Code >= 500 && reply.Code != 530 {
		return fmt.Errorf("%w: smtp %s %s: %v", mail.ErrPermanent, step, t.address, err) //nolint:errorlint // LIB-010
	}
	return fmt.Errorf("smtp %s %s: %v", step, t.address, err) //nolint:errorlint // LIB-010
}
