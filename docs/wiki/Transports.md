# Transports

Every transport implements `mail.Transport` (`Send(ctx, *mail.Message) error`), is safe for concurrent use and never changes the message. Use them behind a `mail.Mailer`, which validates first.

## smtp

```go
transport, err := smtp.New(&smtp.Options{
    Host:     "smtp.example.com",
    Port:     587,                    // default per Security: 587 STARTTLS, 465 TLS, 25 none
    Security: smtp.SecurityStartTLS,  // default; SecurityTLS, SecurityNone
    Username: "tasks", Password: secret,
    // TLSConfig: custom roots, LocalName: EHLO name, Timeout: per send (default 30 s)
})
```

- STARTTLS must be offered by the server; there is no silent downgrade.
- AUTH PLAIN without TLS is refused at `New` unless the host is localhost.
- One connection per message. The context's deadline and cancellation bound every network wait.
- A 5xx reply to MAIL, RCPT or DATA matches `mail.ErrPermanent`. A 5xx to the greeting, STARTTLS or AUTH (a rotated password, a relay that does not know this host) is temporary: it is the transport's configuration, and a queued mail goes out once that is fixed. 4xx, network errors and timeouts are temporary.
- The Message-ID is derived from `IdempotencyKey` when it is set, so every retry of one job carries the same Message-ID and receivers that deduplicate on it drop the copy. Delivery is still at least once.
- At least once: a reply lost after the server accepted the data looks like a failure, and a retry sends the mail again.

## resend

```go
transport, err := resend.New(&resend.Options{APIKey: key}) // BaseURL for tests, HTTPClient optional (30 s timeout)
```

- `Message.IdempotencyKey` is sent as `Idempotency-Key`: Resend delivers a key once for 24 hours and answers a reused key with another payload with 409, which is permanent.
- 4xx is permanent except 401, 403 and 404 (API key, unverified domain, base URL: the transport's configuration), 408, 429 and 409 `concurrent_idempotent_requests`; 5xx and network errors are temporary. The transport never retries or sleeps itself.

## logmailer

```go
transport, err := logmailer.New(&logmailer.Options{Writer: os.Stderr})
```

Writes headers, attachments (name, type, size), the text part and the HTML per mail. For local runs and demos; it sends nothing.

## Tracing and metrics

`oteladapter.NewTransport` wraps any transport in a `mail.send` client span with recipient and attachment counts (no addresses). A failed send has status error, description `permanent failure` or `temporary failure`, and `mail.permanent`; the error text stays out of the trace because it quotes addresses, so log it on the wide event if you need it. `prometheusadapter.Metrics` is an Observer: `<ns>_mail_sends_total{result}` and `<ns>_mail_send_duration_seconds{result}`, result one of `sent`, `invalid`, `rejected`, `failed`.
