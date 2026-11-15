package prometheusadapter_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"

	"github.com/govengers/mail"
	"github.com/govengers/mail/mailtest"
	"github.com/govengers/mail/prometheusadapter"
)

func TestMetricsCountSendsByResult(t *testing.T) {
	registry := prometheus.NewRegistry()
	metrics, err := prometheusadapter.New(&prometheusadapter.Options{Registerer: registry, Namespace: "tasks"})
	require.NoError(t, err)
	recorder := &mailtest.Recorder{}
	mailer, err := mail.New(&mail.Options{Transport: recorder, From: "noreply@example.com", Observer: metrics})
	require.NoError(t, err)
	message := &mail.Message{To: []string{"a@example.com"}, Subject: "Hi", Text: "x"}

	require.NoError(t, mailer.Send(t.Context(), message))
	require.Error(t, mailer.Send(t.Context(), &mail.Message{Subject: "no recipient", Text: "x"}))
	recorder.FailWith(errors.New("down"))
	require.Error(t, mailer.Send(t.Context(), message))

	require.NoError(t, testutil.GatherAndCompare(registry, strings.NewReader(`
# HELP tasks_mail_sends_total Mails handed to the Mailer by result.
# TYPE tasks_mail_sends_total counter
tasks_mail_sends_total{result="failed"} 1
tasks_mail_sends_total{result="invalid"} 1
tasks_mail_sends_total{result="rejected"} 0
tasks_mail_sends_total{result="sent"} 1
`), "tasks_mail_sends_total"))
	require.Equal(t, 3, testutil.CollectAndCount(registry, "tasks_mail_send_duration_seconds"))
}

func TestNewValidatesAndReportsRegistrationErrors(t *testing.T) {
	_, err := prometheusadapter.New(nil)
	require.Error(t, err)
	_, err = prometheusadapter.New(&prometheusadapter.Options{Registerer: prometheus.NewRegistry()})
	require.Error(t, err)

	registry := prometheus.NewRegistry()
	_, err = prometheusadapter.New(&prometheusadapter.Options{Registerer: registry, Namespace: "tasks"})
	require.NoError(t, err)
	_, err = prometheusadapter.New(&prometheusadapter.Options{Registerer: registry, Namespace: "tasks"})
	require.Error(t, err, "a duplicate")

	clash := prometheus.NewRegistry()
	require.NoError(t, clash.Register(prometheus.NewCounter(prometheus.CounterOpts{Name: "tasks_mail_send_duration_seconds", Help: "x"})))
	_, err = prometheusadapter.New(&prometheusadapter.Options{Registerer: clash, Namespace: "tasks"})
	require.Error(t, err)
	_, err = prometheusadapter.New(&prometheusadapter.Options{Registerer: clash, Namespace: "tasks"})
	require.ErrorContains(t, err, "send_duration_seconds", "the counter was unregistered after the first failure")
}
