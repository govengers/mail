package mail

import (
	"errors"
	"fmt"
	htmltemplate "html/template"
	"io"
	"io/fs"
	"net/url"
	"path"
	"strings"
	texttemplate "text/template"

	"github.com/govengers/mail/internal/htmlscan"
)

var (
	// ErrTemplateNotFound is returned by Render for a name without templates.
	ErrTemplateNotFound = errors.New("mail: template not found")
	// ErrUnsafeLink is returned by Render when the HTML holds a link that is not an absolute
	// http, https or mailto URL: a relative link, javascript:, or an empty value. The error names
	// the template and the link's position, never the link: it can carry a bearer token, and the
	// error text ends up in logs and in a job's last error.
	ErrUnsafeLink = errors.New("mail: unsafe link")
)

// commentFunction is the template function that writes a comment of the compiled HTML back.
const commentFunction = "mailComment"

// TemplatesOptions configures Templates.
type TemplatesOptions struct {
	// FS holds the templates in its root, usually an embed.FS behind fs.Sub:
	//
	//	<name>.<locale>.html   compiled by cmd/mailgen from <name>.<locale>.mjml (html/template)
	//	<name>.<locale>.txt    the plain-text part and {{define "subject"}} (text/template)
	//	_layout.txt            optional plain-text layout around {{template "content" .}}
	//	_layout.<locale>.txt   optional plain-text layout for one locale
	//
	// Every other .html or .txt file in the root is an error; other files (the .mjml sources)
	// are ignored.
	FS fs.FS
	// DefaultLocale is the locale every template exists in and the last fallback, e.g. "en".
	DefaultLocale string
}

// Templates renders mails from compiled templates. Rendering is safe for concurrent use.
type Templates struct {
	defaultLocale string
	variants      map[string]map[string]*variant // name, normalized locale
}

// variant is one template in one locale.
type variant struct {
	html *htmltemplate.Template
	text *texttemplate.Template // executes the body; defines "subject"
}

// sources are the files of one template in one locale.
type sources struct {
	html, text string
}

// NewTemplates parses every template of options.FS. It fails for a template without its .html
// or .txt part, without {{define "subject"}}, without a DefaultLocale variant, or that does not
// parse.
func NewTemplates(options *TemplatesOptions) (*Templates, error) {
	if options == nil || options.FS == nil {
		return nil, errors.New("mail: TemplatesOptions.FS is required")
	}
	if !localePattern.MatchString(options.DefaultLocale) {
		return nil, fmt.Errorf("mail: TemplatesOptions.DefaultLocale %q is not a locale", options.DefaultLocale)
	}
	files, layouts, err := readTemplates(options.FS)
	if err != nil {
		return nil, err
	}
	templates := &Templates{defaultLocale: normalizeLocale(options.DefaultLocale), variants: map[string]map[string]*variant{}}
	for name, locales := range files {
		if locales[templates.defaultLocale] == nil {
			return nil, fmt.Errorf("mail: template %s has no %s variant", name, templates.defaultLocale)
		}
		templates.variants[name] = map[string]*variant{}
		for locale, files := range locales {
			parsed, err := parseVariant(name+"."+locale, files, layoutFor(layouts, locale))
			if err != nil {
				return nil, err
			}
			templates.variants[name][locale] = parsed
		}
	}
	return templates, nil
}

// readTemplates reads the root of fsys into the templates by name and locale and the text
// layouts by locale ("" for _layout.txt).
func readTemplates(fsys fs.FS) (map[string]map[string]*sources, map[string]string, error) {
	entries, err := fs.ReadDir(fsys, ".")
	if err != nil {
		return nil, nil, fmt.Errorf("mail: read templates: %w", err)
	}
	files := map[string]map[string]*sources{}
	layouts := map[string]string{}
	for _, entry := range entries {
		fileName := entry.Name()
		extension := path.Ext(fileName)
		if entry.IsDir() || (extension != ".html" && extension != ".txt") {
			continue
		}
		content, err := fs.ReadFile(fsys, fileName)
		if err != nil {
			return nil, nil, fmt.Errorf("mail: read template %s: %w", fileName, err)
		}
		if match := layoutFile.FindStringSubmatch(fileName); match != nil {
			layouts[normalizeLocale(match[1])] = string(content)
			continue
		}
		match := templateFile.FindStringSubmatch(fileName)
		if match == nil {
			return nil, nil, fmt.Errorf("mail: %s is neither <name>.<locale>.html|txt nor a _layout.txt", fileName)
		}
		name, locale := match[1], normalizeLocale(match[2])
		if files[name] == nil {
			files[name] = map[string]*sources{}
		}
		if files[name][locale] == nil {
			files[name][locale] = &sources{}
		}
		if extension == ".html" {
			files[name][locale].html = string(content)
		} else {
			files[name][locale].text = string(content)
		}
	}
	return files, layouts, nil
}

// layoutFor returns the text layout of locale or of a less specific locale, then _layout.txt, or
// "" for none. It never takes another language's layout.
func layoutFor(layouts map[string]string, locale string) string {
	for {
		if layout, ok := layouts[locale]; ok {
			return layout
		}
		cut := strings.LastIndexByte(locale, '-')
		if cut < 0 {
			return layouts[""]
		}
		locale = locale[:cut]
	}
}

func parseVariant(name string, files *sources, layout string) (*variant, error) {
	if files.html == "" || files.text == "" {
		return nil, fmt.Errorf("mail: template %s needs both a non-empty .html and a .txt file", name)
	}
	text, err := parseText(name, files.text, layout)
	if err != nil {
		return nil, err
	}
	html, err := parseHTML(name, files.html)
	if err != nil {
		return nil, err
	}
	return &variant{html: html, text: text}, nil
}

func parseText(name, body, layout string) (*texttemplate.Template, error) {
	root := texttemplate.New(name).Option("missingkey=error")
	content := root
	if layout != "" {
		if _, err := root.Parse(layout); err != nil {
			return nil, fmt.Errorf("mail: parse the text layout of %s: %w", name, err)
		}
		content = root.New("content")
	}
	if _, err := content.Parse(body); err != nil {
		return nil, fmt.Errorf("mail: parse %s.txt: %w", name, err)
	}
	if root.Lookup("subject") == nil {
		return nil, fmt.Errorf(`mail: %s.txt has no {{define "subject"}}`, name)
	}
	return root, nil
}

// parseHTML parses compiled MJML with html/template. html/template drops comments, and MJML's
// output depends on Outlook's conditional comments, so every comment is taken out first and
// written back verbatim by a template function. An action inside a comment is an error.
func parseHTML(name, document string) (*htmltemplate.Template, error) {
	var text strings.Builder
	var comments []string
	position := 0
	for _, comment := range htmlscan.Comments(document) {
		if strings.Contains(document[comment.Start:comment.End], "{{") {
			return nil, fmt.Errorf("mail: %s.html has a template action inside an HTML comment, where it would stay text; "+
				"the MJML compiler puts an mj-raw between sections into Outlook's comments, so loop inside one mj-text instead", name)
		}
		text.WriteString(document[position:comment.Start])
		fmt.Fprintf(&text, "{{%s %d}}", commentFunction, len(comments))
		comments = append(comments, document[comment.Start:comment.End])
		position = comment.End
	}
	text.WriteString(document[position:])
	functions := htmltemplate.FuncMap{commentFunction: func(index int) htmltemplate.HTML {
		return htmltemplate.HTML(comments[index]) //nolint:gosec // G203: the comments come from the compiled template, never from data
	}}
	parsed, err := htmltemplate.New(name).Funcs(functions).Option("missingkey=error").Parse(text.String())
	if err != nil {
		return nil, fmt.Errorf("mail: parse %s.html: %w", name, err)
	}
	// html/template escapes on the first Execute; a dry run without data surfaces an action in a
	// context it cannot escape (an unclosed attribute) now instead of on the first send.
	var escapeError *htmltemplate.Error
	if err := parsed.Execute(io.Discard, nil); errors.As(err, &escapeError) {
		return nil, fmt.Errorf("mail: parse %s.html: %w", name, err)
	}
	return parsed, nil
}

// Has reports whether t holds the template name. Render then finds a variant for every locale,
// because every template exists in the default locale.
func (t *Templates) Has(name string) bool {
	_, ok := t.variants[name]
	return ok
}

// Render renders the template name in locale, falling back to less specific locales and then
// to the default (de-AT, de, en), and returns a Message with Subject, HTML and Text set; the
// caller adds the recipients. The subject is one line with whitespace collapsed, the text part is
// trimmed. It fails with ErrTemplateNotFound for an unknown name, with ErrUnsafeLink for a link in
// the HTML that is not absolute http, https or mailto, and with the template's error for data
// that does not fit (a missing map key is an error). The error never quotes the data: it names the
// template position and the failing action, and drops text/template's reason except for a missing
// key.
func (t *Templates) Render(name, locale string, data any) (*Message, error) {
	locales, ok := t.variants[name]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrTemplateNotFound, name)
	}
	for _, candidate := range fallbacks(locale, t.defaultLocale) {
		if found := locales[candidate]; found != nil {
			return found.render(data)
		}
	}
	return nil, fmt.Errorf("%w: %s.%s", ErrTemplateNotFound, name, t.defaultLocale)
}

func (v *variant) render(data any) (*Message, error) {
	var subject, text, html strings.Builder
	if err := v.text.ExecuteTemplate(&subject, "subject", data); err != nil {
		return nil, fmt.Errorf("mail: render the subject: %w", withoutData(err))
	}
	if err := v.text.Execute(&text, data); err != nil {
		return nil, fmt.Errorf("mail: render the text: %w", withoutData(err))
	}
	if err := v.html.Execute(&html, data); err != nil {
		return nil, fmt.Errorf("mail: render the HTML: %w", withoutData(err))
	}
	for index, link := range htmlscan.Links(html.String()) {
		if !safeLink(link) {
			return nil, fmt.Errorf("%w: link %d in %s is not an absolute http, https or mailto URL", ErrUnsafeLink, index+1, v.html.Name())
		}
	}
	return &Message{
		Subject: strings.Join(strings.Fields(subject.String()), " "),
		HTML:    html.String(),
		Text:    strings.TrimSpace(text.String()) + "\n",
	}, nil
}

// withoutData cuts a template execution error after the failing action. The standard library's
// reason can quote the data ("range can't iterate over <value>", eq's "non-comparable type
// <value>"), and the data can carry a bearer token (M19). The location and the action are template
// source, and a missing key's reason names only the template's field, so those are kept.
func withoutData(err error) error {
	var execError texttemplate.ExecError
	if !errors.As(err, &execError) {
		return err // an escape error of the template source, it holds no data
	}
	// "template: name:1:2: executing \"name\" at <action>: reason". The first ">: " is never after
	// the action's end, so the kept part never holds data. A missing key's reason is kept only when
	// that ">: " is the only one: a string literal in the action can hold ">: " and the prefix too.
	message := execError.Error()
	end := strings.Index(message, ">: ")
	if end < 0 {
		return fmt.Errorf("template: %s: reason not shown, it can quote the data", execError.Name)
	}
	reason := message[end+len(">: "):]
	missingKey := strings.HasPrefix(reason, "map has no entry for key ") || strings.HasPrefix(reason, "nil data; no entry for key ")
	if missingKey && strings.Count(message, ">: ") == 1 {
		return err
	}
	return errors.New(message[:end+1] + ": reason not shown, it can quote the data")
}

// safeLink reports whether link is an absolute http or https URL with a host, or a mailto URL.
func safeLink(link string) bool {
	parsed, err := url.Parse(strings.TrimSpace(link))
	if err != nil {
		return false
	}
	switch strings.ToLower(parsed.Scheme) {
	case "http", "https":
		return parsed.Host != ""
	case "mailto":
		return parsed.Opaque != ""
	default:
		return false
	}
}
