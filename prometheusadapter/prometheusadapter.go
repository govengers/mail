// Package prometheusadapter exports the Mailer's sends as Prometheus metrics on a registry the
// caller passes in (LIB-011). Metrics implements mail.Observer; give it to mail.Options.Observer.
package prometheusadapter

import (
	"errors"
	"fmt"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/govengers/mail"
)

const subsystem = "mail"

// sendBuckets reach from 50 ms to about 50 s: a local relay answers in milliseconds, an API or a
// remote SMTP server with TLS in seconds.
var sendBuckets = prometheus.ExponentialBuckets(0.05, 2.5, 9)

// Options configures Metrics.
type Options struct {
	Registerer prometheus.Registerer // required; the caller's registry, never the default one
	Namespace  string                // required: the service name, e.g. "tasks" for tasks_mail_sends_total
}

// Metrics counts and times sends; it implements mail.Observer and is safe for concurrent use.
//
//	<namespace>_mail_sends_total{result}        result: sent, invalid, rejected, failed
//	<namespace>_mail_send_duration_seconds{result}
type Metrics struct {
	sends     *prometheus.CounterVec
	durations *prometheus.HistogramVec
}

// New registers the metrics on options.Registerer; a failed registration is returned as an error.
func New(options *Options) (*Metrics, error) {
	if options == nil || options.Registerer == nil {
		return nil, errors.New("prometheusadapter: Registerer is required")
	}
	if options.Namespace == "" {
		return nil, errors.New("prometheusadapter: Namespace is required")
	}
	metrics := &Metrics{
		sends: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: options.Namespace, Subsystem: subsystem, Name: "sends_total",
			Help: "Mails handed to the Mailer by result.",
		}, []string{"result"}),
		durations: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: options.Namespace, Subsystem: subsystem, Name: "send_duration_seconds",
			Help: "Duration of a send, validation and transport, by result.", Buckets: sendBuckets,
		}, []string{"result"}),
	}
	if err := options.Registerer.Register(metrics.sends); err != nil {
		return nil, fmt.Errorf("prometheusadapter: register %s_%s_sends_total: %v", options.Namespace, subsystem, err) //nolint:errorlint // LIB-010
	}
	if err := options.Registerer.Register(metrics.durations); err != nil {
		options.Registerer.Unregister(metrics.sends)
		return nil, fmt.Errorf("prometheusadapter: register %s_%s_send_duration_seconds: %v", options.Namespace, subsystem, err) //nolint:errorlint // LIB-010
	}
	for _, result := range []mail.Result{mail.ResultSent, mail.ResultInvalid, mail.ResultRejected, mail.ResultFailed} {
		metrics.sends.WithLabelValues(string(result)) // every result has a series from the start, so rate() and alerts see zero
	}
	return metrics, nil
}

// SendFinished implements mail.Observer.
func (m *Metrics) SendFinished(outcome *mail.Outcome) {
	result := string(outcome.Result)
	m.sends.WithLabelValues(result).Inc()
	m.durations.WithLabelValues(result).Observe(outcome.Duration.Seconds())
}
