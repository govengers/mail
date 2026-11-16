# Gotchas

- **Loops between sections do not work.** gomjml drops text between components and puts an `mj-raw` between sections inside Outlook's conditional comments. Loop inside one `mj-text` or `mj-table`. `NewTemplates` refuses an action inside a comment.
- **One `mj-column` per `mj-section`** for stacked content; more columns render side by side.
- **Commit the HTML and run `mailgen -check` in lint.** The service embeds the `.html`; a forgotten compile ships the old mail.
- **SMTP retries can duplicate.** Use the resend transport where a duplicate matters, or accept at least once.
- **The subject lives in the `.txt` file** as `{{define "subject"}}`; trim the newline after it with `{{end -}}`.
- **Relative links fail the render.** Pass absolute URLs (the service's public base URL from configuration) in the data.
- **A job's template data is in clear text without a Sealer.** A reset or invitation link in the data sits in the jobs table and its admin listing; configure `jobsadapter.Options.Sealer` or pass a reference and resolve it with `Options.Resolve`.
- **Job templates see the data's JSON form** unless a resolve function decodes it: `{{.name}}` for a field tagged `json:"name"`, numbers as `json.Number`. `sender.Template` renders that form at enqueue, so a missing key fails there, but a zero number is true in `{{if}}` and `{{if gt .N 0}}` fails on the type; pass a bool or preformatted text.
- **Rename a job template when its data changes incompatibly.** Jobs enqueued with the old data may still be pending; a missing key in the job is permanent.
- **Pass the Mailer, not the transport, as `jobsadapter.Options.Transport`**, or messages without a From are not filled in and sends are not observed.
