# Gotchas

- **Loops between sections do not work.** gomjml drops text between components and puts an `mj-raw` between sections inside Outlook's conditional comments. Loop inside one `mj-text` or `mj-table`. `NewTemplates` refuses an action inside a comment.
- **One `mj-column` per `mj-section`** for stacked content; more columns render side by side.
- **Commit the HTML and run `mailgen -check` in lint.** The service embeds the `.html`; a forgotten compile ships the old mail.
- **SMTP retries can duplicate.** Use the resend transport where a duplicate matters, or accept at least once.
- **The subject lives in the `.txt` file** as `{{define "subject"}}`; trim the newline after it with `{{end -}}`.
- **Relative links fail the render.** Pass absolute URLs (the service's public base URL from configuration) in the data.
- **The job payload is the rendered message**, so it is capped at 256 KiB by govengers/jobs and a template change does not affect mails already queued.
- **Pass the Mailer, not the transport, to `jobsadapter.Handle`**, or messages without a From are not filled in and sends are not observed.
