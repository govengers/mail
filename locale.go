package mail

import (
	"regexp"
	"strings"
)

// templateFile matches <name>.<locale>.html and <name>.<locale>.txt; cmd/mailgen uses the same
// rule for its .mjml sources. Names starting with an underscore are layouts, never templates.
var templateFile = regexp.MustCompile(`^([A-Za-z0-9][A-Za-z0-9_-]*)\.([A-Za-z]{2,3}(?:[-_][A-Za-z0-9]{1,8})*)\.(html|txt)$`)

// layoutFile matches the plain-text layout, _layout.txt or _layout.<locale>.txt.
var layoutFile = regexp.MustCompile(`^_layout(?:\.([A-Za-z]{2,3}(?:[-_][A-Za-z0-9]{1,8})*))?\.txt$`)

// localePattern is a BCP 47-like language tag: en, de-AT, pt_BR, zh-Hant-TW.
var localePattern = regexp.MustCompile(`^[A-Za-z]{2,3}(?:[-_][A-Za-z0-9]{1,8})*$`)

// normalizeLocale lowercases a tag and writes its separators as hyphens: pt_BR becomes pt-br.
func normalizeLocale(locale string) string {
	return strings.ToLower(strings.ReplaceAll(locale, "_", "-"))
}

// fallbacks returns the locales to try for a requested one, most specific first, ending with the
// default: de-at-x, de-at, de, en. An empty or malformed request tries the default only.
func fallbacks(requested, defaultLocale string) []string {
	var chain []string
	if localePattern.MatchString(requested) {
		locale := normalizeLocale(requested)
		for {
			chain = append(chain, locale)
			cut := strings.LastIndexByte(locale, '-')
			if cut < 0 {
				break
			}
			locale = locale[:cut]
		}
	}
	return append(chain, defaultLocale)
}
