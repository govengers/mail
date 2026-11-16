# govengers/mail

Transactional mail for govengers services. Read [docs/design.md](../design.md) for the decisions (M1 onwards).

## Which piece for which need

| Need | Use |
|---|---|
| send a mail now, the caller waits | `mailer.Send(ctx, message)` |
| send a mail after a change commits, with retries | `jobsadapter.New` + `sender.Template(name, locale, data, envelope)` + `store.Enqueue(ctx, tx, jobsadapter.Kind.New(payload, nil))`, see [Jobs](Jobs.md) |
| production transport | `smtp.New` (any provider, at least once) or `resend.New` (idempotent retries), see [Transports](Transports.md) |
| local development | `logmailer.New(&logmailer.Options{Writer: os.Stderr})` |
| render a mail | `mail.NewTemplates` + `Render(name, locale, data)`, see [Templates](Templates.md) |
| metrics | `prometheusadapter.New` as `mail.Options.Observer` |
| traces | `oteladapter.NewTransport` around the transport |
| tests | `mailtest.Recorder`, `mailtest.NewSMTPServer`, `mailtest.NewResendServer`, see [Testing](Testing.md) |

## Wiring in main

```go
transport, err := smtp.New(&smtp.Options{Host: config.SMTPHost, Username: config.SMTPUser, Password: config.SMTPPassword})
// or resend.New(&resend.Options{APIKey: config.ResendAPIKey}), or logmailer.New in development
traced, err := oteladapter.NewTransport(&oteladapter.Options{Transport: transport, TracerProvider: tracerProvider, Name: "smtp"})
metrics, err := prometheusadapter.New(&prometheusadapter.Options{Registerer: registry, Namespace: "tasks"})
mailer, err := mail.New(&mail.Options{Transport: traced, From: config.MailFrom, Observer: metrics})
templates, err := mail.NewTemplates(&mail.TemplatesOptions{FS: templateFiles, DefaultLocale: "en"})
sender, err := jobsadapter.New(&jobsadapter.Options{Transport: mailer, Templates: templates, Sealer: sealer})
err = sender.Handle(worker, jobsadapter.Kind)
```

Every constructor validates its options and returns an error; nothing connects before the first send. The library never logs (LIB-003): outcomes go to the Observer, errors to the caller.

## Pages

- [Templates](Templates.md): MJML sources, layouts, locales, mailgen, the drift check, rendering rules.
- [Transports](Transports.md): smtp, resend, logmailer, error classification, delivery guarantees.
- [Jobs](Jobs.md): sending from a govengers/jobs job.
- [Testing](Testing.md): Recorder, fake servers, Parse, Links, keeping tests fast.
- [Gotchas](Gotchas.md): the rules that are easy to get wrong.
