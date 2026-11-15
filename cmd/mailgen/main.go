// Command mailgen compiles the MJML mail templates of a directory to HTML, or checks that the
// committed HTML is what the sources compile to. Applications run it at build time, pinned to a
// version, and commit its output; nothing at runtime needs Node or an MJML engine:
//
//	go run github.com/govengers/mail/cmd/mailgen@<version> -dir internal/mail/templates
//	go run github.com/govengers/mail/cmd/mailgen@<version> -dir internal/mail/templates -check
//
// For every <name>.<locale>.mjml it writes <name>.<locale>.html next to it. A source that is a
// whole document (it starts with <mjml) compiles as it is. Any other source is the body of a
// layout: _layout.<locale>.mjml, a less specific locale's layout, or _layout.mjml. The layout is a
// whole MJML document with the marker <!-- mail:content --> in its mj-body and, optionally,
// <!-- mail:head --> in its mj-head; a body source may start with an <mj-head> element whose
// children replace the head marker (a per-mail mj-preview or mj-title).
//
// Template actions ({{.Name}}) pass through MJML as text: put them in the content of mj-text,
// mj-button and mj-table or in attribute values. Text between components is dropped, and an
// mj-raw between sections lands inside Outlook's conditional comments, so loops and conditions go
// inside one mj-text or mj-table: <mj-text>{{range .Items}}<p>{{.}}</p>{{end}}</mj-text>.
// mail.NewTemplates refuses an action inside a comment.
//
// -check compiles in memory and exits with status 1 when an .html file differs from its source,
// is missing, or has no source. The MJML engine is github.com/preslavrachev/gomjml at the version
// this module's go.mod pins, so a given mailgen version always writes the same bytes.
package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/preslavrachev/gomjml/mjml"
)

const (
	contentMarker = "<!-- mail:content -->"
	headMarker    = "<!-- mail:head -->"
)

// localePattern is the locale rule of github.com/govengers/mail's Templates.
const localePattern = `[A-Za-z]{2,3}(?:[-_][A-Za-z0-9]{1,8})*`

var (
	sourceFile   = regexp.MustCompile(`^([A-Za-z0-9][A-Za-z0-9_-]*)\.(` + localePattern + `)\.mjml$`)
	compiledFile = regexp.MustCompile(`^([A-Za-z0-9][A-Za-z0-9_-]*)\.(` + localePattern + `)\.html$`)
	layoutFile   = regexp.MustCompile(`^_layout(?:\.(` + localePattern + `))?\.mjml$`)
	bodyHead     = regexp.MustCompile(`(?s)^\s*<mj-head>(.*?)</mj-head>`)
)

// errDrift is returned by run in check mode when the HTML does not match the sources.
var errDrift = errors.New("the compiled HTML differs from the MJML sources; run mailgen without -check")

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "mailgen:", err)
		os.Exit(1)
	}
}

func run(arguments []string, output io.Writer) error {
	flags := flag.NewFlagSet("mailgen", flag.ContinueOnError)
	flags.SetOutput(output)
	directory := flags.String("dir", ".", "directory of the .mjml sources; the .html files are written next to them")
	check := flags.Bool("check", false, "write nothing; fail when an .html file is not what its source compiles to")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	compiled, err := compileDirectory(*directory)
	if err != nil {
		return err
	}
	if *check {
		return checkDirectory(*directory, compiled, output)
	}
	return writeDirectory(*directory, compiled, output)
}

// compileDirectory compiles every source of directory and returns the HTML by file name.
func compileDirectory(directory string) (map[string][]byte, error) {
	entries, err := os.ReadDir(directory)
	if err != nil {
		return nil, err
	}
	layouts := map[string]string{}
	var sources []string
	for _, entry := range entries {
		switch name := entry.Name(); {
		case entry.IsDir():
		case layoutFile.MatchString(name):
			content, err := os.ReadFile(filepath.Join(directory, name))
			if err != nil {
				return nil, err
			}
			layouts[normalizeLocale(layoutFile.FindStringSubmatch(name)[1])] = string(content)
		case sourceFile.MatchString(name):
			sources = append(sources, name)
		}
	}
	compiled := map[string][]byte{}
	for _, name := range sources {
		html, err := compileSource(directory, name, layouts)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		compiled[strings.TrimSuffix(name, ".mjml")+".html"] = html
	}
	return compiled, nil
}

func compileSource(directory, name string, layouts map[string]string) ([]byte, error) {
	source, err := os.ReadFile(filepath.Join(directory, name))
	if err != nil {
		return nil, err
	}
	document := string(source)
	if !strings.HasPrefix(strings.TrimSpace(document), "<mjml") {
		document, err = composeLayout(document, layoutFor(layouts, normalizeLocale(sourceFile.FindStringSubmatch(name)[2])))
		if err != nil {
			return nil, err
		}
	}
	html, err := mjml.Render(document)
	if err != nil {
		return nil, fmt.Errorf("compile: %w", err)
	}
	return []byte(strings.TrimSpace(html) + "\n"), nil
}

// composeLayout puts a body source into its layout.
func composeLayout(body, layout string) (string, error) {
	if layout == "" {
		return "", errors.New("not a whole <mjml> document and no _layout.mjml for its locale")
	}
	if strings.Count(layout, contentMarker) != 1 {
		return "", fmt.Errorf("the layout needs %s exactly once", contentMarker)
	}
	head := ""
	if match := bodyHead.FindStringSubmatchIndex(body); match != nil {
		head, body = body[match[2]:match[3]], body[match[1]:]
	}
	switch {
	case strings.Contains(layout, headMarker):
		layout = strings.Replace(layout, headMarker, head, 1)
	case strings.TrimSpace(head) != "":
		return "", fmt.Errorf("the source has an <mj-head> but the layout has no %s", headMarker)
	}
	return strings.Replace(layout, contentMarker, body, 1), nil
}

// layoutFor returns the layout of locale or a less specific locale, then _layout.mjml, or "".
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

func normalizeLocale(locale string) string {
	return strings.ToLower(strings.ReplaceAll(locale, "_", "-"))
}

func writeDirectory(directory string, compiled map[string][]byte, output io.Writer) error {
	for _, name := range sortedKeys(compiled) {
		if err := os.WriteFile(filepath.Join(directory, name), compiled[name], 0o644); err != nil { //nolint:gosec // G306: templates are committed source files
			return err
		}
		fmt.Fprintln(output, "wrote", name)
	}
	return nil
}

func checkDirectory(directory string, compiled map[string][]byte, output io.Writer) error {
	var problems []string
	for _, name := range sortedKeys(compiled) {
		committed, err := os.ReadFile(filepath.Join(directory, name))
		switch {
		case errors.Is(err, os.ErrNotExist):
			problems = append(problems, name+": missing")
		case err != nil:
			return err
		case !bytes.Equal(committed, compiled[name]):
			problems = append(problems, name+": differs from its source")
		}
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if compiledFile.MatchString(entry.Name()) && compiled[entry.Name()] == nil {
			problems = append(problems, entry.Name()+": no .mjml source")
		}
	}
	for _, problem := range problems {
		fmt.Fprintln(output, problem)
	}
	if len(problems) > 0 {
		return errDrift
	}
	return nil
}

func sortedKeys(compiled map[string][]byte) []string {
	names := make([]string, 0, len(compiled))
	for name := range compiled {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
