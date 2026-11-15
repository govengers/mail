# Mail from a job

`github.com/govengers/mail/jobsadapter` is a module of its own: only services that use govengers/jobs pull in its dependencies.

```go
// main; jobsadapter.Kind is "mail.send" on the queue "mail"
if err := jobsadapter.Handle(worker, jobsadapter.Kind, mailer); err != nil {
    return err
}

// service method, inside the transaction that creates the user
message, err := templates.Render("welcome", user.Locale, data)
if err != nil {
    return err
}
message.To = []string{user.Email}
_, err = store.Enqueue(ctx, transaction, jobsadapter.Kind.New(*message, nil))
```

A service that wants another queue, attempt count or backoff declares its own kind and passes it to `Handle` and `Enqueue`:

```go
var MailKind = jobs.NewKind[mail.Message]("mail.send", &jobs.KindOptions{Queue: "notifications", MaximumAttempts: 10})
```

- The mail is rendered at enqueue and stored as the job payload (the message as JSON). A rolled-back transaction enqueues nothing; a committed one sends after the commit.
- Each attempt sends with the job's ID as `IdempotencyKey` (unless the message carries one): with the resend transport, retries deliver once. With smtp, delivery is at least once, and the copy carries the same Message-ID.
- `mail.ErrPermanent` (invalid message, rejected sender, recipient or content) ends the job as dead at once; other errors, a refused password or API key included, retry with the kind's backoff. Pass the `*mail.Mailer`, not the bare transport, so validation and metrics apply.
- govengers/jobs caps a payload at 256 KiB: keep attachments small or store them in blob storage and send a reference with a handler of your own.
- In service tests, drain the worker with `jobstest.Drain` and assert on a `mailtest.Recorder` behind the Mailer.
