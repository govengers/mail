package main

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

const layout = `<mjml lang="en">
  <mj-head><!-- mail:head --></mj-head>
  <mj-body>
    <!-- mail:content -->
  </mj-body>
</mjml>
`

const body = `<mj-head><mj-preview>Preview of {{.Name}}</mj-preview></mj-head>
<mj-section><mj-column><mj-text>Hello {{.Name}}</mj-text></mj-column></mj-section>
`

func writeFiles(t *testing.T, files map[string]string) string {
	t.Helper()
	directory := t.TempDir()
	for name, content := range files {
		require.NoError(t, os.WriteFile(filepath.Join(directory, name), []byte(content), 0o600))
	}
	return directory
}

func mailgen(t *testing.T, arguments ...string) (string, error) {
	t.Helper()
	var output strings.Builder
	err := run(arguments, &output)
	return output.String(), err
}

func TestCompileWritesHTMLAndCheckAcceptsIt(t *testing.T) {
	directory := writeFiles(t, map[string]string{
		"_layout.mjml":    layout,
		"welcome.en.mjml": body,
		"whole.en.mjml":   `<mjml><mj-body><mj-section><mj-column><mj-text>{{.X}}</mj-text></mj-column></mj-section></mj-body></mjml>`,
		"_partial.mjml":   "ignored",
		"notes.md":        "ignored",
	})

	output, err := mailgen(t, "-dir", directory)
	require.NoError(t, err)
	require.Equal(t, "wrote welcome.en.html\nwrote whole.en.html\n", output)

	html, err := fs.ReadFile(os.DirFS(directory), "welcome.en.html")
	require.NoError(t, err)
	require.Contains(t, string(html), "Hello {{.Name}}")
	require.Contains(t, string(html), "Preview of {{.Name}}")
	require.Contains(t, string(html), `lang="en"`)
	require.True(t, strings.HasSuffix(string(html), "</html>\n"))

	output, err = mailgen(t, "-dir", directory, "-check")
	require.NoError(t, err)
	require.Empty(t, output)
}

func TestCheckReportsDrift(t *testing.T) {
	directory := writeFiles(t, map[string]string{"_layout.mjml": layout, "a.en.mjml": body, "b.de.mjml": body})
	_, err := mailgen(t, "-dir", directory)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(directory, "a.en.html"), []byte("edited by hand"), 0o600))
	require.NoError(t, os.Remove(filepath.Join(directory, "b.de.html")))
	require.NoError(t, os.WriteFile(filepath.Join(directory, "c.en.html"), []byte("<p>orphan</p>"), 0o600))

	output, err := mailgen(t, "-dir", directory, "-check")

	require.ErrorIs(t, err, errDrift)
	require.Equal(t, "a.en.html: differs from its source\nb.de.html: missing\nc.en.html: no .mjml source\n", output)
}

func TestLayoutsFollowTheLocale(t *testing.T) {
	directory := writeFiles(t, map[string]string{
		"_layout.mjml":    strings.Replace(layout, `lang="en"`, `lang="und"`, 1),
		"_layout.de.mjml": strings.Replace(layout, `lang="en"`, `lang="de"`, 1),
		"a.de-AT.mjml":    body,
		"a.en.mjml":       body,
	})

	_, err := mailgen(t, "-dir", directory)
	require.NoError(t, err)

	austrian, err := fs.ReadFile(os.DirFS(directory), "a.de-AT.html")
	require.NoError(t, err)
	require.Contains(t, string(austrian), `lang="de"`)
	english, err := fs.ReadFile(os.DirFS(directory), "a.en.html")
	require.NoError(t, err)
	require.Contains(t, string(english), `lang="und"`)
}

func TestCompileRefusesBrokenSources(t *testing.T) {
	for name, files := range map[string]map[string]string{
		"no layout":          {"a.en.mjml": body},
		"no content marker":  {"_layout.mjml": "<mjml><mj-body></mj-body></mjml>", "a.en.mjml": body},
		"two content marker": {"_layout.mjml": "<mjml><mj-body><!-- mail:content --><!-- mail:content --></mj-body></mjml>", "a.en.mjml": body},
		"no head marker":     {"_layout.mjml": "<mjml><mj-body><!-- mail:content --></mj-body></mjml>", "a.en.mjml": body},
		"a directory":        {"_layout.mjml/x": ""},
	} {
		directory := t.TempDir()
		for file, content := range files {
			require.NoError(t, os.MkdirAll(filepath.Dir(filepath.Join(directory, file)), 0o700))
			require.NoError(t, os.WriteFile(filepath.Join(directory, file), []byte(content), 0o600))
		}
		_, err := mailgen(t, "-dir", directory)
		if name == "a directory" {
			require.NoError(t, err, "a directory named like a layout is skipped")
			continue
		}
		require.Error(t, err, name)
	}
}

func TestRunRejectsBadArguments(t *testing.T) {
	_, err := mailgen(t, "-unknown")
	require.Error(t, err)
	_, err = mailgen(t, "-dir", filepath.Join(t.TempDir(), "missing"))
	require.Error(t, err)
}
