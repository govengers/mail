package htmlscan_test

import (
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
		`<p>text href="https://no.example" src=x</p>a <b <i/>`
	require.Equal(t, []string{
		"https://a.example/?x=1&y=2", "https://b.example/i.png", "https://c.example/bg.png", "https://d.example/v.png", "mailto:x@example.com",
	}, htmlscan.Links(document))
}
