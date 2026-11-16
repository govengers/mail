package mail_test

import (
	"os"
	"sync"
	"testing"
	"testing/fstest"

	"github.com/stretchr/testify/require"

	"github.com/govengers/mail"
)

type welcomeData struct {
	Product    string
	Name       string
	ConfirmURL string
	Tasks      []string
}

func newTestdataTemplates(t *testing.T) *mail.Templates {
	t.Helper()
	templates, err := mail.NewTemplates(&mail.TemplatesOptions{FS: os.DirFS("testdata/templates"), DefaultLocale: "en"})
	require.NoError(t, err)
	return templates
}

func TestRenderFillsSubjectHTMLAndTextFromTheCompiledTemplates(t *testing.T) {
	templates := newTestdataTemplates(t)

	message, err := templates.Render("welcome", "en", &welcomeData{
		Product: "Tasks", Name: "Ada <script>", ConfirmURL: "https://example.com/confirm?token=a&b=c", Tasks: []string{"one", "two"},
	})

	require.NoError(t, err)
	require.Equal(t, "Welcome to Tasks, Ada <script>", message.Subject)
	require.Equal(t, "Tasks\n\nHello Ada <script>,\n\nplease confirm your address: https://example.com/confirm?token=a&b=c\n\n- one\n- two\n\n--\nSent by Tasks\n", message.Text)
	require.Contains(t, message.HTML, "Hello Ada &lt;script&gt;,")
	require.Contains(t, message.HTML, `href="https://example.com/confirm?token=a&amp;b=c"`)
	require.Contains(t, message.HTML, "<!--[if mso | IE]>", "the conditional comments for Outlook survive html/template")
	require.Contains(t, message.HTML, "Confirm your address to start", "the preview from the source's mj-head")
	require.Contains(t, message.HTML, "<p>one</p><p>two</p>")
	require.Empty(t, message.To, "the caller adds the recipients")
}

func TestRenderFallsBackToALessSpecificLocaleThenTheDefault(t *testing.T) {
	templates := newTestdataTemplates(t)
	data := &welcomeData{Product: "Tasks", Name: "Ada", ConfirmURL: "https://example.com/c"}

	for requested, subject := range map[string]string{
		"de":    "Willkommen bei Tasks, Ada",
		"de-AT": "Willkommen bei Tasks, Ada",
		"DE_at": "Willkommen bei Tasks, Ada",
		"fr":    "Welcome to Tasks, Ada",
		"":      "Welcome to Tasks, Ada",
		"../x":  "Welcome to Tasks, Ada",
	} {
		message, err := templates.Render("welcome", requested, data)
		require.NoError(t, err, requested)
		require.Equal(t, subject, message.Subject, requested)
	}
}

func TestRenderOfAWholeMJMLDocumentCollapsesTheSubjectToOneLine(t *testing.T) {
	templates := newTestdataTemplates(t)

	message, err := templates.Render("reset", "en", map[string]string{"Product": "Tasks", "ResetURL": "https://example.com/r"})

	require.NoError(t, err)
	require.Equal(t, "Reset your password", message.Subject)
	require.Equal(t, "Tasks\n\nReset your password: https://example.com/r\n\n--\nSent by Tasks\n", message.Text)
}

func TestRenderRefusesUnsafeLinks(t *testing.T) {
	templates := newTestdataTemplates(t)

	for _, link := range []string{"javascript:alert(1)", "/relative", "", "https://", "ftp://example.com/x", "data:text/html,x"} {
		_, err := templates.Render("reset", "en", map[string]string{"Product": "Tasks", "ResetURL": link})
		require.ErrorIs(t, err, mail.ErrUnsafeLink, link)
	}
	_, err := templates.Render("reset", "en", map[string]string{"Product": "Tasks", "ResetURL": "/reset#token=bearer-secret"})
	require.ErrorIs(t, err, mail.ErrUnsafeLink)
	require.NotContains(t, err.Error(), "bearer-secret", "the link can carry a token, so the error does not quote it")
	_, err = templates.Render("reset", "en", map[string]string{"Product": "Tasks", "ResetURL": "HTTPS://example.com/x"})
	require.NoError(t, err)
}

func TestRenderFailsForAnUnknownTemplateAndMissingData(t *testing.T) {
	templates := newTestdataTemplates(t)

	_, err := templates.Render("unknown", "en", nil)
	require.ErrorIs(t, err, mail.ErrTemplateNotFound)
	require.False(t, templates.Has("unknown"))
	require.True(t, templates.Has("reset"))

	_, err = templates.Render("reset", "en", map[string]string{"Product": "Tasks"})
	require.ErrorContains(t, err, `map has no entry for key "ResetURL"`, "a missing key's reason names only the field")
}

// text/template's reason for a failed action can print the data ("range can't iterate over
// <value>"), and the data can hold a bearer link, so Render keeps only the location and the action.
func TestARenderErrorNamesTheActionButNeverQuotesTheData(t *testing.T) {
	text := `{{define "subject"}}S{{end}}body`
	templates, err := mail.NewTemplates(&mail.TemplatesOptions{FS: fstest.MapFS{
		"range.en.html":   file(`<p>{{range .Link}}{{.}}{{end}}</p>`),
		"range.en.txt":    file(text),
		"eq.en.html":      file(`<p>x</p>`),
		"eq.en.txt":       file(`{{define "subject"}}S{{end}}{{if eq .Link .Link}}same{{end}}`),
		"subject.en.html": file(`<p>x</p>`),
		"subject.en.txt":  file(`{{define "subject"}}{{range .Link}}{{.}}{{end}}{{end}}body`),
	}, DefaultLocale: "en"})
	require.NoError(t, err)

	for name, data := range map[string]any{
		"range":   map[string]any{"Link": "https://tasks.example.com/accept#token=bearer-secret"},
		"eq":      map[string]any{"Link": map[string]any{"u": "https://tasks.example.com/accept#token=bearer-secret"}},
		"subject": map[string]any{"Link": "https://tasks.example.com/accept#token=bearer-secret"},
	} {
		_, err := templates.Render(name, "en", data)
		require.ErrorContains(t, err, "at <", name)
		require.ErrorContains(t, err, ".Link", name)
		require.NotContains(t, err.Error(), "bearer-secret", name)
	}
}

func TestRenderIsSafeForConcurrentUse(t *testing.T) {
	templates := newTestdataTemplates(t)
	var group sync.WaitGroup
	for range 8 {
		group.Go(func() {
			_, err := templates.Render("welcome", "de", &welcomeData{Product: "Tasks", Name: "Ada", ConfirmURL: "https://example.com/c"})
			require.NoError(t, err)
		})
	}
	group.Wait()
}

func TestNewTemplatesRejectsIncompleteSets(t *testing.T) {
	html := "<p>{{.X}}</p>"
	text := `{{define "subject"}}S{{end}}body`
	for name, files := range map[string]fstest.MapFS{
		"no default locale":  {"a.de.html": file(html), "a.de.txt": file(text)},
		"no text part":       {"a.en.html": file(html)},
		"no html part":       {"a.en.txt": file(text)},
		"no subject":         {"a.en.html": file(html), "a.en.txt": file("body")},
		"bad file name":      {"a.en.html": file(html), "a.en.txt": file(text), "notes.txt": file("x")},
		"bad text template":  {"a.en.html": file(html), "a.en.txt": file(`{{define "subject"}}S{{end}}{{.X`)},
		"bad html template":  {"a.en.html": file("{{.X"), "a.en.txt": file(text)},
		"bad layout":         {"a.en.html": file(html), "a.en.txt": file(text), "_layout.txt": file("{{")},
		"html in a bad spot": {"a.en.html": file(`<a href="{{.X}}`), "a.en.txt": file(text)},
		"action in comment":  {"a.en.html": file(`<!--[if mso]>{{.X}}<![endif]-->`), "a.en.txt": file(text)},
	} {
		_, err := mail.NewTemplates(&mail.TemplatesOptions{FS: files, DefaultLocale: "en"})
		require.Error(t, err, name)
	}
}

func TestNewTemplatesValidatesItsOptions(t *testing.T) {
	_, err := mail.NewTemplates(nil)
	require.Error(t, err)
	_, err = mail.NewTemplates(&mail.TemplatesOptions{FS: fstest.MapFS{}, DefaultLocale: "english!"})
	require.Error(t, err)
	_, err = mail.NewTemplates(&mail.TemplatesOptions{FS: os.DirFS("does-not-exist"), DefaultLocale: "en"})
	require.Error(t, err)
}

func TestTextLayoutsFollowTheTemplatesLocale(t *testing.T) {
	text := `{{define "subject"}}S{{end}}body`
	templates, err := mail.NewTemplates(&mail.TemplatesOptions{FS: fstest.MapFS{
		"a.en.html":      file("<p>x</p>"),
		"a.en.txt":       file(text),
		"a.de-at.html":   file("<p>x</p>"),
		"a.de-at.txt":    file(text),
		"_layout.de.txt": file(`DE {{template "content" .}}`),
		"a.en.mjml":      file("ignored"),
	}, DefaultLocale: "en"})
	require.NoError(t, err)

	german, err := templates.Render("a", "de-AT", nil)
	require.NoError(t, err)
	require.Equal(t, "DE body\n", german.Text)
	english, err := templates.Render("a", "en", nil)
	require.NoError(t, err)
	require.Equal(t, "body\n", english.Text, "no generic layout, and the German one is not taken")
}

func file(content string) *fstest.MapFile {
	return &fstest.MapFile{Data: []byte(content)}
}
