# mail

[![CI](https://github.com/govengers/mail/actions/workflows/ci.yml/badge.svg?branch=main)](https://github.com/govengers/mail/actions/workflows/ci.yml)
[![unit tests](https://img.shields.io/endpoint?url=https://raw.githubusercontent.com/govengers/mail/badges/unit-tests.json)](https://github.com/govengers/mail/actions/workflows/ci.yml)
[![e2e tests](https://img.shields.io/endpoint?url=https://raw.githubusercontent.com/govengers/mail/badges/e2e-tests.json)](test/e2e)
[![coverage](https://img.shields.io/endpoint?url=https://raw.githubusercontent.com/govengers/mail/badges/coverage.json)](https://github.com/govengers/mail/actions/workflows/ci.yml)
[![Go Reference](https://pkg.go.dev/badge/github.com/govengers/mail.svg)](https://pkg.go.dev/github.com/govengers/mail)
[![License](https://img.shields.io/github/license/govengers/mail)](LICENSE)

Coding agents: start at [llms.txt](llms.txt) and [docs/wiki/Home.md](docs/wiki/Home.md).

Transactional mail for govengers services: one `Message`, SMTP and Resend transports, MJML templates compiled at build time and rendered per locale at runtime, and a govengers/jobs adapter that sends from a job enqueued in the caller's transaction. Module `github.com/govengers/mail`, Go 1.26, Apache 2.0.

```sh
go get github.com/govengers/mail
```

## Packages

| Package | Module | What | Depends on |
|---|---|---|---|
| `mail` | root | `Message`, `Mailer` (validation, default sender, `Observer`), `Templates` (locale fallback, plain-text layout, link check), `ErrPermanent` | standard library |
| `mail/smtp` | root | SMTP transport: STARTTLS (default), implicit TLS or none, AUTH PLAIN, one connection per mail | standard library |
| `mail/resend` | root | Resend HTTP API transport with `Idempotency-Key` | standard library |
| `mail/logmailer` | root | development transport: writes every mail to a writer | standard library |
| `mail/mailtest` | root | `Recorder`, fake SMTP and Resend servers, `Parse`, `Links`, `TLSConfigs` | standard library |
| `mail/prometheusadapter` | root | `Metrics`, an `Observer` on the caller's registry | client_golang |
| `mail/oteladapter` | root | `NewTransport`: a client span per send | OpenTelemetry API |
| `mail/jobsadapter` | `github.com/govengers/mail/jobsadapter` | `Handle`: send from a govengers/jobs job, job ID as idempotency key | govengers/jobs |
| `cmd/mailgen` | `github.com/govengers/mail/cmd/mailgen` | build-time MJML compiler with a drift check | gomjml |

A service that only sends over SMTP imports `mail` and `mail/smtp` and builds with the standard library alone.

## Sending

```go
// main
transport, err := smtp.New(&smtp.Options{Host: config.SMTPHost, Username: config.SMTPUser, Password: config.SMTPPassword})
if err != nil {
    return err
}
metrics, err := prometheusadapter.New(&prometheusadapter.Options{Registerer: registry, Namespace: "tasks"})
if err != nil {
    return err
}
mailer, err := mail.New(&mail.Options{Transport: transport, From: "Tasks <noreply@tasks.example.com>", Observer: metrics})
if err != nil {
    return err
}

// a service
err = mailer.Send(ctx, &mail.Message{To: []string{user.Email}, Subject: "Hello", Text: "Hi\n"})
if errors.Is(err, mail.ErrPermanent) {
    // invalid message or rejected recipient: retrying cannot help
}
```

`Mailer.Send` copies the message, fills in the default `From`, validates it (addresses, one-line subject, no header injection, safe attachment names) and hands it to the transport. Errors that match `mail.ErrPermanent` cannot succeed on a retry: an invalid message (`mail.ErrInvalidMessage`), an SMTP 5xx reply to MAIL, RCPT or DATA, a Resend 4xx other than 401, 403, 404, 408, 429 and a concurrent-key 409. A misconfigured transport (refused greeting, STARTTLS or AUTH, a wrong API key) is temporary, so queued mails go out once it is fixed. Everything else may be temporary.

## Templates

Templates are authored in MJML (one `mj-column` per `mj-section` for stacked content) and compiled to HTML at build time, so nothing at runtime needs Node or an MJML engine:

```text
internal/mail/templates/
  _layout.mjml            shared frame with <!-- mail:content --> (and optionally <!-- mail:head -->)
  welcome.en.mjml         body sections; an optional leading <mj-head> fills the head marker
  welcome.en.txt          {{define "subject"}}...{{end}} and the plain-text part (text/template)
  welcome.de.mjml / .txt  another locale
  _layout.txt             optional plain-text frame around {{template "content" .}}
  welcome.en.html         written by mailgen, committed
```

```sh
go run github.com/govengers/mail/cmd/mailgen@<version> -dir internal/mail/templates          # after editing
go run github.com/govengers/mail/cmd/mailgen@<version> -dir internal/mail/templates -check   # in just lint: fails on drift
```

```go
//go:embed templates/*.html templates/*.txt
var files embed.FS

sub, _ := fs.Sub(files, "templates")
templates, err := mail.NewTemplates(&mail.TemplatesOptions{FS: sub, DefaultLocale: "en"})

message, err := templates.Render("welcome", user.Locale, data) // de-AT, then de, then en
message.To = []string{user.Email}
```

HTML renders with `html/template` (data is escaped; Outlook's conditional comments survive), the text part with `text/template`. Every `href`, `src`, `background`, `action`, `formaction`, `poster` and `srcset` URL in the rendered HTML must be an absolute `http`, `https` or `mailto` URL, or `Render` fails with `mail.ErrUnsafeLink`. A missing map key is an error, not `<no value>`.

## Mail from a job

```go
err := jobsadapter.Handle(worker, jobsadapter.Kind, mailer)                     // main
_, err = store.Enqueue(ctx, transaction, jobsadapter.Kind.New(*message, nil))   // in the user's transaction
```

`jobsadapter.Kind` is `mail.send` on the queue `mail`; declare your own `jobs.NewKind[mail.Message]` for another queue or backoff. The job's ID becomes the message's `IdempotencyKey`, so the Resend transport delivers once across retries. SMTP has no idempotency: a retry after a lost reply delivers twice (at least once), with the same Message-ID. A permanent failure ends the job as dead at once.

## Tests

Unit tests use `mailtest.Recorder`; an end-to-end suite points the binary at `mailtest.NewSMTPServer` or `mailtest.NewResendServer` and reads the delivered mails back with `mailtest.Parse` and `mailtest.Links`. The library's own tests need no network, no Docker and no database: `just test` runs in a few seconds.

## Docs

[docs/wiki/Home.md](docs/wiki/Home.md) (package map, wiring), [Templates](docs/wiki/Templates.md), [Transports](docs/wiki/Transports.md), [Jobs](docs/wiki/Jobs.md), [Testing](docs/wiki/Testing.md), [Gotchas](docs/wiki/Gotchas.md), and the binding design with decisions M1 onwards in [docs/design.md](docs/design.md).

## License

Apache 2.0, see [LICENSE](LICENSE).
