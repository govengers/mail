// Package htmlscan finds comments and link attributes in the HTML that MJML generates. It is a
// scanner for that well-formed, generated markup, not an HTML parser.
package htmlscan

import (
	"html"
	"regexp"
	"strings"
)

// linkAttribute matches href, src and background attributes with a double-quoted, single-quoted
// or unquoted value.
var linkAttribute = regexp.MustCompile(`(?i)[\s"'](?:href|src|background)\s*=\s*(?:"([^"]*)"|'([^']*)'|([^\s"'>]+))`)

// Range is the half-open byte range [Start, End) of a match.
type Range struct {
	Start, End int
}

// Comments returns the ranges of every comment (<!-- ... -->), in order, outside the raw text of
// style and script elements. An unterminated comment runs to the end of the document.
func Comments(document string) []Range {
	var comments []Range
	lower := strings.ToLower(document)
	for position := 0; position < len(document); {
		next := strings.IndexByte(document[position:], '<')
		if next < 0 {
			break
		}
		position += next
		switch {
		case strings.HasPrefix(document[position:], "<!--"):
			end := strings.Index(document[position+len("<!--"):], "-->")
			if end < 0 {
				return append(comments, Range{Start: position, End: len(document)})
			}
			end += position + len("<!--") + len("-->")
			comments = append(comments, Range{Start: position, End: end})
			position = end
		case isRawTextStart(lower[position:], "style"), isRawTextStart(lower[position:], "script"):
			position = skipRawText(lower, position)
		default:
			position++
		}
	}
	return comments
}

// isRawTextStart reports whether rest starts with the start tag of element.
func isRawTextStart(rest, element string) bool {
	if !strings.HasPrefix(rest, "<"+element) || len(rest) == len(element)+1 {
		return false
	}
	switch rest[len(element)+1] {
	case '>', ' ', '\t', '\n', '\r', '/':
		return true
	default:
		return false
	}
}

// skipRawText returns the position after the end tag of the raw text element that starts at
// position, or the end of the document.
func skipRawText(lower string, position int) int {
	element := "style"
	if strings.HasPrefix(lower[position:], "<script") {
		element = "script"
	}
	end := strings.Index(lower[position:], "</"+element)
	if end < 0 {
		return len(lower)
	}
	return position + end + len("</"+element)
}

// Links returns the HTML-unescaped values of every href, src and background attribute, in order.
// Attributes inside comments count too: Outlook reads conditional comments.
func Links(document string) []string {
	var links []string
	for _, match := range linkAttribute.FindAllStringSubmatch(document, -1) {
		links = append(links, html.UnescapeString(match[1]+match[2]+match[3]))
	}
	return links
}
