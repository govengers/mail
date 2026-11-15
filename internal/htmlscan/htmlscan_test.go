package htmlscan_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/govengers/mail/internal/htmlscan"
)

func TestComments(t *testing.T) {
	for name, testCase := range map[string]struct {
		document string
		comments []string
	}{
		"none":        {document: "<p>a < b</p>"},
		"conditional": {document: `<!--[if mso]><table><![endif]--><p>x</p><!--[if !mso]><!--><b>y</b><!--<![endif]-->`, comments: []string{"<!--[if mso]><table><![endif]-->", "<!--[if !mso]><!-->", "<!--<![endif]-->"}},
		"style":       {document: `<style>/* <!-- not a comment --> */</style><!--a-->`, comments: []string{"<!--a-->"}},
		"script":      {document: `<SCRIPT type="x"><!--x--></script><!--b-->`, comments: []string{"<!--b-->"}},
		"stylesheet":  {document: `<stylesheet><!--c-->`, comments: []string{"<!--c-->"}},
		"open style":  {document: `<style><!--d-->`},
		"unclosed":    {document: `<p>x</p><!--e`, comments: []string{"<!--e"}},
		"tag at end":  {document: `<style`},
		// strings.ToLower grows U+0130 by a byte and shrinks U+1E9E and U+212A; offsets must hold.
		"dotted capital I": {document: `<title>İİİİİİİ</title><style>a{}<!-- x --></style><!--f-->`, comments: []string{"<!--f-->"}},
		"capital sharp s":  {document: `<p>` + strings.Repeat("ẞ", 10) + `</p><!--g--></html>`, comments: []string{"<!--g-->"}},
		"kelvin sign":      {document: `<p>` + strings.Repeat("\u212a", 10) + `</p><STYLE><!--h--></STYLE><!--i-->`, comments: []string{"<!--i-->"}},
	} {
		var found []string
		for _, comment := range htmlscan.Comments(testCase.document) {
			found = append(found, testCase.document[comment.Start:comment.End])
		}
		require.Equal(t, testCase.comments, found, name)
	}
}

func TestLinks(t *testing.T) {
	document := `<a href="https://a.example/?x=1&amp;y=2">a</a><img src='https://b.example/i.png'><td background=https://c.example/bg.png>` +
		`<!--[if mso]><v:image src="https://d.example/v.png" /><![endif]--><a HREF = "mailto:x@example.com">m</a><div data-x="href=no" title='a src=b' hidden>` +
		`<p>text href="https://no.example" src=x</p>a <b <i/>` +
		`<img srcset="https://e.example/1.png 1x, /relative.png 2x"><form action="javascript:x"><button formaction=/f></form><video poster="p.png">`
	require.Equal(t, []string{
		"https://a.example/?x=1&y=2", "https://b.example/i.png", "https://c.example/bg.png", "https://d.example/v.png", "mailto:x@example.com",
		"https://e.example/1.png", "/relative.png", "javascript:x", "/f", "p.png",
	}, htmlscan.Links(document))
}
