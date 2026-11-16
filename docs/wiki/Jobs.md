# Mail from a job

`github.com/govengers/mail/jobsadapter` is a module of its own: only services that use govengers/jobs pull in its dependencies.

```go
// main; jobsadapter.Kind is "mail.send" on the queue "mail"
sender, err := jobsadapter.New(&jobsadapter.Options{Transport: mailer, Templates: templates, Sealer: sealer})
if err != nil {
    return err
}
if err := sender.Handle(worker, jobsadapter.Kind); err != nil {
    return err
}

// service method, inside the transaction that creates the reset request
payload, err := sender.Template("reset", user.Locale, data, &mail.Message{To: []string{user.Email}})
if err != nil {
    return err
}
_, err = store.Enqueue(ctx, transaction, jobsadapter.Kind.New(payload, nil))
```

A service that wants another queue, attempt count or backoff declares its own kind and passes it to `Handle` and `Enqueue`:

```go
var MailKind = jobs.NewKind[jobsadapter.Payload]("mail.send", &jobs.KindOptions{Queue: "notifications", MaximumAttempts: 10})
```

## The payload is a reference

The job payload (`jobsadapter.Payload`) names the template and locale, carries the envelope (`To`, `From`, `ReplyTo`, `Headers`, `Attachments`, `IdempotencyKey`) and the template data as JSON. The handler renders the mail when the job runs, so no rendered body and no bearer link sits in the jobs table or its admin listing (standards JOB-006).

- `sender.Template` checks the template name, so a typo fails in the caller's transaction, not after every attempt of the job. It copies the envelope: reusing one `*mail.Message` for a batch is safe.
- A template without a resolve function (below) sees the data's JSON form: field names as their JSON keys, numbers as `json.Number` (every digit kept). `sender.Template` renders that form once at enqueue, so a missing key or an unsafe link fails in the caller's transaction, not later in the job.
- The JSON form is not your type. A zero number is true in `{{if .Unread}}` (`json.Number` is a string), `{{if gt .Unread 0}}` fails with "incompatible types for comparison", a field tagged `json:"resetUrl"` is `.resetUrl`, methods and `time.Time` are gone. Pass the data preformatted (`"UnreadText": "3 unread"`, a bool for each condition), or give the template a resolve function that decodes into your type, so it sees the same data as with `templates.Render`.
- A rolled-back transaction enqueues nothing; a committed one sends after the commit.
- A template change reaches mails already queued. A template whose data changes incompatibly gets a new name (`reset-v2`), because jobs with the old data may still be pending.

## Secrets in the data

A reset token or an invitation link is a secret: it must not sit in the payload in clear text. Two ways:

1. **Seal the data.** With `Options.Sealer` every template mail's data is sealed (`Payload.SealedData`), bound to its template, locale and recipients, so sealed data copied into a job for another template, locale or recipient does not open. A job with the same three accepts it (a replay of an older mail to the same person). govengers/auth's seal package has another method set; wrap its key ring:

   ```go
   type authSealer struct{ ring *seal.Sealer }

   func (s authSealer) data(associatedData []byte) seal.AssociatedData {
       return seal.AssociatedData{Purpose: "mail_template_data", Table: "jobs", Row: string(associatedData)}
   }

   func (s authSealer) Seal(plaintext, associatedData []byte) ([]byte, error) {
       sealed, err := s.ring.Seal(s.data(associatedData), plaintext)
       return []byte(sealed), err
   }

   func (s authSealer) Open(sealed, associatedData []byte) ([]byte, error) {
       return s.ring.Open(s.data(associatedData), string(sealed))
   }
   ```

   A worker without the Sealer or without the key retries, so a key that arrives with the next rollout still sends the mail. The text of an `Open` error is stored as the job's last error: it must not contain the plaintext.

2. **Pass a reference.** The data carries an ID (of the reset request), and the template's resolve function in `Options.Resolve` loads the secret when the job runs. It receives the stored JSON:

   ```go
   Resolve: map[string]jobsadapter.ResolveFunc{
       "reset": func(ctx context.Context, data json.RawMessage) (any, error) {
           var reference struct{ RequestID string }
           if err := json.Unmarshal(data, &reference); err != nil {
               return nil, jobs.Permanent(err)
           }
           request, err := resets.Get(ctx, reference.RequestID)
           if errors.Is(err, ErrNotFound) {
               return nil, jobs.Permanent(err) // used or revoked: send nothing
           }
           if err != nil {
               return nil, err // retries
           }
           return &ResetMail{Name: request.Name, ResetURL: request.URL()}, nil
       },
   },
   ```

   A template with a resolve function is not rendered at enqueue: the referenced row may exist only in the caller's uncommitted transaction. The other templates still are. The error text is stored as the job's last error: it must not contain the secret.

## Pre-rendered messages

A mail without secrets may be rendered by the caller and sent as it is: `jobsadapter.Kind.New(jobsadapter.Payload{Message: *message}, nil)`. The payload then holds the whole message, attachments included.

## Sending

- Each attempt sends with the job's ID as `IdempotencyKey` (unless the envelope carries one): with the resend transport, retries deliver once. With smtp, delivery is at least once, and the copy carries the same Message-ID.
- The mail is rendered per attempt. If an attempt was delivered but its reply was lost and the retry renders another body (a resolve function loaded changed data, a deploy changed the template), Resend refuses it with 409 `invalid_idempotent_request`. That is permanent: the job is dead although the mail went out once.
- The handler's error is stored as the job's last error and shown in the admin listing. `mail.ErrUnsafeLink` names the link's position, not the link, so a bearer link does not land there.
- `mail.ErrPermanent` (invalid message, rejected sender, recipient or content) and data that does not render end the job as dead at once. Other errors retry with the kind's backoff: a refused password or API key, a template the worker does not know yet (an older replica during a rolling deploy), missing Templates or Sealer, a sealing key the worker does not hold, a `Resolve` error that is not permanent.
- Pass the `*mail.Mailer` as `Options.Transport`, not the bare transport, so the default From, validation and metrics apply.
- govengers/jobs caps a payload at 256 KiB: keep attachments small, or store the file in blob storage and link to it from the mail. To attach it anyway, wrap `sender.Handler()` in a handler of your own that loads the file into `job.Payload.Message.Attachments` and then calls it; attachments are not part of the Sealer's associated data, so sealed data still opens.
- In service tests, drain the worker with `jobstest.Drain` and assert on a `mailtest.Recorder` behind the Mailer.
