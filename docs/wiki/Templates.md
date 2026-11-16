# Templates

## Files

One directory per service, usually `internal/mail/templates`:

| File | Written by | Read by |
|---|---|---|
| `_layout.mjml`, `_layout.<locale>.mjml` | you | mailgen |
| `<name>.<locale>.mjml` | you | mailgen |
| `<name>.<locale>.html` | mailgen, committed | `mail.Templates` (html/template) |
| `<name>.<locale>.txt` | you | `mail.Templates` (text/template) |
| `_layout.txt`, `_layout.<locale>.txt` | you, optional | `mail.Templates` |

Names are letters, digits, `-` and `_`; a name starting with `_` is a layout. Locales are tags like `en`, `de-AT`, `pt_BR` (case and `_` vs `-` do not matter). `NewTemplates` refuses any other `.html` or `.txt` file in the directory, a template without both parts, a `.txt` without `{{define "subject"}}`, and a template missing in the default locale.

## Layout

```xml
<!-- _layout.mjml -->
<mjml lang="en">
  <mj-head>
    <mj-attributes><mj-all font-family="Arial, sans-serif" /></mj-attributes>
    <!-- mail:head -->
  </mj-head>
  <mj-body background-color="#f4f4f5">
    <mj-section><mj-column><mj-text>{{.Product}}</mj-text></mj-column></mj-section>
    <!-- mail:content -->
  </mj-body>
</mjml>
```

```xml
<!-- welcome.en.mjml -->
<mj-head><mj-preview>Confirm your address</mj-preview></mj-head>
<mj-section>
  <mj-column>
    <mj-text>Hello {{.Name}},</mj-text>
    <mj-button href="{{.ConfirmURL}}">Confirm</mj-button>
  </mj-column>
</mj-section>
```

A source picks `_layout.<its locale>.mjml`, then the layout of a less specific locale, then `_layout.mjml`. A source that starts with `<mjml` is a whole document and uses no layout. One `mj-column` per `mj-section` for stacked content: several columns in one section render side by side.

## Compiling

```sh
go run github.com/govengers/mail/cmd/mailgen@<version> -dir internal/mail/templates
go run github.com/govengers/mail/cmd/mailgen@<version> -dir internal/mail/templates -check
```

Pin the version in the justfile; run the first after editing a source, the second in `just lint`. The check fails when an `.html` differs from what its source compiles to, is missing, or has no source. Embed only the `.html` and `.txt` files.

## Plain text and subject

```text
{{define "subject"}}Welcome to {{.Product}}, {{.Name}}{{end -}}
Hello {{.Name}},

please confirm your address: {{.ConfirmURL}}
```

The subject is collapsed to one line. With a `_layout.txt`, the file's body is the template `content` the layout calls with `{{template "content" .}}`.

## Rendering

`templates.Render(name, locale, data)` tries the locale, its parents (`de-AT`, `de`), then the default, and returns a `*mail.Message` with `Subject`, `HTML` and `Text`. Data in HTML is escaped by `html/template`; a URL in an attribute is filtered. A missing map key is an error. Every `href`, `src`, `background`, `action`, `formaction`, `poster` and `srcset` URL in the result must be absolute `http`, `https` or `mailto`, or `Render` returns `mail.ErrUnsafeLink`. The error names the template and the link's position, not the link, because a link can carry a token. For the same reason a template execution error names the position and the action (`at <.Links>`) but not text/template's reason, which can print the value; only a missing key's reason (`map has no entry for key "ResetURL"`) is kept. `templates.Has(name)` checks a name without rendering.

## Template actions in MJML

Actions pass through the compiler as text inside `mj-text`, `mj-button`, `mj-table` and attribute values. Text between components is dropped, and an `mj-raw` between sections ends up inside Outlook's conditional comments, where an action would stay text (`NewTemplates` refuses that). Put loops and conditions inside one component:

```xml
<mj-text>{{range .Tasks}}<p>{{.Title}}</p>{{end}}</mj-text>
```
