//go:build e2e

// Package e2e runs the library the way a service does: the mailgen binary compiles the MJML
// templates, Templates renders them, and a Mailer with metrics and tracing sends them through the
// smtp transport (STARTTLS and AUTH) and the resend transport to fakes that speak the protocols.
package e2e

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/govengers/mail"
	"github.com/govengers/mail/mailtest"
	"github.com/govengers/mail/oteladapter"
	"github.com/govengers/mail/prometheusadapter"
	"github.com/govengers/mail/resend"
	"github.com/govengers/mail/smtp"
)

const testdata = "../../testdata/templates"

type welcome struct {
	Product, Name, ConfirmURL string
	Tasks                     []string
}

// compiled is the directory mailgen compiled the templates into, once for the suite.
var compiled string

// TestMain compiles the testdata sources with the mailgen binary, as a service's build does.
func TestMain(m *testing.M) {
	directory, err := compileTemplates()
	if err != nil {
		fmt.Fprintln(os.Stderr, "e2e:", err)
		os.Exit(1)
	}
	compiled = directory
	code := m.Run()
	_ = os.RemoveAll(directory)
	os.Exit(code)
}

// compileTemplates copies the MJML and text sources to a fresh directory, compiles them with the
// mailgen binary, checks them with -check and compares them with the committed HTML.
func compileTemplates() (string, error) {
	mailgen := os.Getenv("E2E_MAILGEN")
	if mailgen == "" {
		return "", errors.New("E2E_MAILGEN names the mailgen binary; run just test-e2e")
	}
	directory, err := os.MkdirTemp("", "mail-e2e-")
	if err != nil {
		return "", err
	}
	if err := copySources(directory); err != nil {
		return "", err
	}
	for _, arguments := range [][]string{{"-dir", directory}, {"-dir", directory, "-check"}} {
		if output, err := exec.CommandContext(context.Background(), mailgen, arguments...).CombinedOutput(); err != nil { //nolint:gosec // G702: the suite runs the mailgen binary just e2e-build built
			return "", fmt.Errorf("mailgen %v: %w: %s", arguments, err, output)
		}
	}
	return directory, compareWithCommitted(directory)
}

func copySources(directory string) error {
	entries, err := os.ReadDir(testdata)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if extension := filepath.Ext(entry.Name()); extension != ".mjml" && extension != ".txt" {
			continue
		}
		content, err := fs.ReadFile(os.DirFS(testdata), entry.Name())
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(directory, entry.Name()), content, 0o600); err != nil {
			return err
		}
	}
	return nil
}

func compareWithCommitted(directory string) error {
	for _, name := range []string{"welcome.en.html", "welcome.de.html", "reset.en.html"} {
		fresh, err := fs.ReadFile(os.DirFS(directory), name)
		if err != nil {
			return err
		}
		committed, err := fs.ReadFile(os.DirFS(testdata), name)
		if err != nil {
			return err
		}
		if !bytes.Equal(fresh, committed) {
			return fmt.Errorf("testdata %s is not what mailgen writes; run just mjml", name)
		}
	}
	return nil
}

func TestARenderedMailTravelsOverSMTPWithMetricsAndTraces(t *testing.T) {
	templates, err := mail.NewTemplates(&mail.TemplatesOptions{FS: os.DirFS(compiled), DefaultLocale: "en"})
	require.NoError(t, err)
	serverTLS, clientTLS := mailtest.TLSConfigs(t)
	server := mailtest.NewSMTPServer(t, &mailtest.SMTPServerOptions{TLSConfig: serverTLS, Username: "tasks", Password: "secret"})
	transport, err := smtp.New(&smtp.Options{Host: server.Host(), Port: server.Port(), Username: "tasks", Password: "secret", TLSConfig: clientTLS})
	require.NoError(t, err)
	spans := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(spans))
	t.Cleanup(func() { _ = provider.Shutdown(t.Context()) })
	traced, err := oteladapter.NewTransport(&oteladapter.Options{Transport: transport, TracerProvider: provider, Name: "smtp"})
	require.NoError(t, err)
	registry := prometheus.NewRegistry()
	metrics, err := prometheusadapter.New(&prometheusadapter.Options{Registerer: registry, Namespace: "tasks"})
	require.NoError(t, err)
	mailer, err := mail.New(&mail.Options{Transport: traced, From: "Tasks <noreply@example.com>", Observer: metrics})
	require.NoError(t, err)

	message, err := templates.Render("welcome", "de-AT", &welcome{Product: "Tasks", Name: "Jörg", ConfirmURL: "https://tasks.example.com/confirm?token=abc"})
	require.NoError(t, err)
	message.To = []string{"Jörg <joerg@example.com>"}
	require.NoError(t, mailer.Send(t.Context(), message))

	received := server.Messages()
	require.Len(t, received, 1)
	delivered := received[0].Message
	require.Equal(t, "Willkommen bei Tasks, Jörg", delivered.Subject)
	require.Equal(t, message.Text, delivered.Text)
	require.Equal(t, message.HTML, delivered.HTML)
	require.Contains(t, delivered.HTML, "Hallo Jörg,")
	require.Contains(t, mailtest.Links(delivered), "https://tasks.example.com/confirm?token=abc")
	require.NoError(t, testutil.GatherAndCompare(registry, strings.NewReader(`
# HELP tasks_mail_sends_total Mails handed to the Mailer by result.
# TYPE tasks_mail_sends_total counter
tasks_mail_sends_total{result="failed"} 0
tasks_mail_sends_total{result="invalid"} 0
tasks_mail_sends_total{result="rejected"} 0
tasks_mail_sends_total{result="sent"} 1
`), "tasks_mail_sends_total"))
	require.Len(t, spans.Ended(), 1)
}

func TestAResendRetryWithTheSameKeyDeliversOnce(t *testing.T) {
	templates, err := mail.NewTemplates(&mail.TemplatesOptions{FS: os.DirFS(compiled), DefaultLocale: "en"})
	require.NoError(t, err)
	server := mailtest.NewResendServer(t, &mailtest.ResendServerOptions{APIKey: "re_test"})
	transport, err := resend.New(&resend.Options{APIKey: "re_test", BaseURL: server.URL()})
	require.NoError(t, err)
	mailer, err := mail.New(&mail.Options{Transport: transport, From: "noreply@example.com"})
	require.NoError(t, err)

	message, err := templates.Render("reset", "en", map[string]string{"Product": "Tasks", "ResetURL": "https://tasks.example.com/reset"})
	require.NoError(t, err)
	message.To = []string{"ada@example.com"}
	message.IdempotencyKey = "reset/ada/1"
	require.NoError(t, mailer.Send(t.Context(), message))
	require.NoError(t, mailer.Send(t.Context(), message))

	require.Len(t, server.Requests(), 2)
	delivered := server.Messages()
	require.Len(t, delivered, 1)
	require.Equal(t, "Reset your password", delivered[0].Subject)
	require.True(t, strings.HasPrefix(delivered[0].Text, "Tasks\n"))
}
