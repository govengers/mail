// Package htmlscan finds comments and link attributes in the HTML that MJML generates. It is a
// scanner for that well-formed, generated markup, not an HTML parser.
package htmlscan

import (
	"html"
	"strings"
)

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
// It reads attributes of start tags only, so text, which html/template escapes, never yields a link.
// Tags inside comments count too: Outlook reads conditional comments.
func Links(document string) []string {
	var links []string
	for position := 0; position < len(document); position++ {
		if document[position] != '<' || position+1 == len(document) || !isLetter(document[position+1]) {
			continue
		}
		for _, attribute := range attributes(document[position+1:]) {
			switch strings.ToLower(attribute.name) {
			case "href", "src", "background":
				links = append(links, html.UnescapeString(attribute.value))
			}
		}
	}
	return links
}

type attribute struct {
	name, value string
}

// attributes reads the attributes of the start tag that rest begins with, after the "<", up to
// its ">".
func attributes(rest string) []attribute {
	position := strings.IndexAny(rest, " \t\n\r\f/>")
	if position < 0 {
		return nil
	}
	var found []attribute
	for position < len(rest) {
		position = skip(rest, position, " \t\n\r\f/")
		if position >= len(rest) || rest[position] == '>' {
			break
		}
		nameEnd := position + strings.IndexAny(rest[position:]+">", " \t\n\r\f/=>")
		name := rest[position:nameEnd]
		position = skip(rest, nameEnd, " \t\n\r\f")
		if position >= len(rest) || rest[position] != '=' {
			found = append(found, attribute{name: name})
			continue
		}
		var value string
		value, position = attributeValue(rest, skip(rest, position+1, " \t\n\r\f"))
		found = append(found, attribute{name: name, value: value})
	}
	return found
}

// attributeValue reads a quoted or unquoted value at position and returns it and the position after it.
func attributeValue(rest string, position int) (string, int) {
	if position >= len(rest) {
		return "", position
	}
	if quote := rest[position]; quote == '"' || quote == '\'' {
		end := strings.IndexByte(rest[position+1:], quote)
		if end < 0 {
			return rest[position+1:], len(rest)
		}
		return rest[position+1 : position+1+end], position + end + 2
	}
	end := position + strings.IndexAny(rest[position:]+">", " \t\n\r\f>")
	return rest[position:end], end
}

func skip(rest string, position int, characters string) int {
	for position < len(rest) && strings.IndexByte(characters, rest[position]) >= 0 {
		position++
	}
	return position
}

func isLetter(character byte) bool {
	return ('a' <= character && character <= 'z') || ('A' <= character && character <= 'Z')
}
