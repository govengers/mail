// Package mail sends transactional mail: a Message, a Mailer that validates it and reports every
// send to an Observer, and Templates that render MJML-compiled HTML and a plain-text part per
// locale.
//
// Transports live in subpackages: smtp (none, STARTTLS or implicit TLS), resend (the Resend HTTP
// API with idempotency keys), logmailer (development: writes every mail to a writer) and mailtest
// (captures mails, fake SMTP and Resend servers for tests). jobsadapter, a separate module, sends
// mail from a govengers/jobs job enqueued in the caller's transaction. MJML sources are compiled at
// build time by cmd/mailgen, a separate module, so nothing here needs Node or an MJML engine at
// runtime.
//
// The package depends on the standard library only.
package mail
