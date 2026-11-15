# Testing

## In a service

| Test | Transport |
|---|---|
| service and controller tests | `&mailtest.Recorder{}` behind the real `mail.Mailer`; `FailWith(err)` to simulate a failure |
| end-to-end suite (the real binary) | configure SMTP at `mailtest.NewSMTPServer(t, nil)` (or with `TLSConfigs` for STARTTLS), or Resend at `mailtest.NewResendServer(t, nil).URL()` |
| a confirmation link | `mailtest.Links(message)`, then follow the one with your prefix |

`SMTPServer.Messages()` returns the envelope, the raw data and the parsed `*mail.Message` (`mailtest.Parse`). `SMTPServerOptions.Reply` answers a chosen command with a reply line, to test a 550 or a 451. `ResendServer` delivers an idempotency key once, like Resend, and `ResendServerOptions.Reply` answers with an error status.

Template tests: build `mail.NewTemplates` on the embedded files in a test; it fails on any broken template, so one test covers every template's structure. Render each template once with realistic data to catch missing keys.

## In this repo

- `just test`: unit tests of all three modules with the race detector and coverage gates; a few seconds, no network beyond loopback, no Docker, no database.
- `just test-e2e`: builds mailgen and the suite, compiles the testdata, sends through smtp and resend to the fakes; skipped when nothing it runs changed (TST-015).
- `just lint` also runs `mailgen -check` on `testdata/templates`; run `just mjml` after editing a testdata source.
