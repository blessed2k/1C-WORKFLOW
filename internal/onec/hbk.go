package onec

import (
	"archive/zip"
	"bytes"
	"errors"
	"html"
	"io"
	"regexp"
	"strings"
)

// SyntaxEntry is one parsed member of the 1C syntax help (a global function,
// object method/property, constructor, event, type, language operator or query
// keyword).
type SyntaxEntry struct {
	NameRu       string `json:"nameRu"`
	NameEn       string `json:"nameEn,omitempty"`
	Owner        string `json:"owner,omitempty"` // parent object (Russian), e.g. Глобальный контекст
	Kind         string `json:"kind,omitempty"`  // method, property, constructor, event, type, operator, query
	Signature    string `json:"signature,omitempty"`
	Params       string `json:"params,omitempty"`
	Returns      string `json:"returns,omitempty"`
	Description  string `json:"description,omitempty"`
	Example      string `json:"example,omitempty"`
	Availability string `json:"availability,omitempty"`
	Since        string `json:"since,omitempty"`
}

// ParseHBK reads a syntax-help .hbk container and returns all member entries.
// defaultKind is used when the page kind cannot be inferred from its path (pass
// "" for the main context shcntx, "operator" for shlang, "query" for shquery).
func ParseHBK(data []byte, defaultKind string) ([]SyntaxEntry, error) {
	entries, err := ReadContainer(data)
	if err != nil {
		return nil, err
	}
	fs, ok := entries["FileStorage"]
	if !ok {
		return nil, errors.New("no FileStorage in container")
	}
	zr, err := zip.NewReader(bytes.NewReader(fs), int64(len(fs)))
	if err != nil {
		return nil, err
	}

	var out []SyntaxEntry
	for _, zf := range zr.File {
		rc, err := zf.Open()
		if err != nil {
			continue
		}
		b, _ := io.ReadAll(rc)
		rc.Close()
		s := string(b)
		if !looksLikeHTML(s) {
			continue
		}
		if e, ok := parseHelpPage(s, zf.Name, defaultKind); ok {
			out = append(out, e)
		}
	}
	return out, nil
}

var (
	reHeading   = regexp.MustCompile(`(?s)<p class="V8SH_heading">(.*?)</p>`)
	reTitle     = regexp.MustCompile(`(?is)<(?:p|div) class="V8SH_title">(.*?)</(?:p|div)>`)
	rePagetitle = regexp.MustCompile(`(?s)<h1 class="V8SH_pagetitle">(.*?)</h1>`)
	reH1Any     = regexp.MustCompile(`(?is)<h1[^>]*>(.*?)</h1>`)
	reChapter   = regexp.MustCompile(`(?s)<p class="V8SH_chapter">(.*?)</p>`)
	reBold      = regexp.MustCompile(`(?is)<b>\s*([^<:]+?):\s*(?:<br\s*/?>)?\s*</b>`)
	reRubric    = regexp.MustCompile(`<div class="V8SH_rubric"[^>]*>`)
	reBr        = regexp.MustCompile(`(?i)<br\s*/?>`)
	reTag       = regexp.MustCompile(`<[^>]*>`)
	reSpaces    = regexp.MustCompile(`[ \t]+`)
	reIdent     = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9]*$`)
)

// looksLikeHTML reports whether the content is an HTML help page (some pages are
// stored without a .html extension).
func looksLikeHTML(s string) bool {
	head := s
	if len(head) > 400 {
		head = head[:400]
	}
	l := strings.ToLower(head)
	return strings.Contains(l, "<html") || strings.Contains(l, "<h1")
}

// parseHelpPage extracts a member from one help HTML page. For the main context
// (defaultKind == "") only member pages carrying a V8SH_heading are kept, so
// category/section pages are skipped.
func parseHelpPage(page, path, defaultKind string) (SyntaxEntry, bool) {
	rawName := firstMatch(reHeading, page)
	if rawName == "" {
		if defaultKind == "" {
			return SyntaxEntry{}, false
		}
		if rawName = firstMatch(rePagetitle, page); rawName == "" {
			rawName = firstMatch(reH1Any, page)
		}
	}
	if rawName == "" {
		return SyntaxEntry{}, false
	}

	nameRu, nameEn := splitRuEn(cleanInline(rawName))
	if nameRu == "" {
		return SyntaxEntry{}, false
	}
	if defaultKind == "query" {
		nameRu = strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(strings.TrimPrefix(
			nameRu, "Функция "), "Оператор "), "Ключевое слово "))
	}
	if nameEn == "" {
		if base := identBase(path); base != "" {
			nameEn = base
		}
	}

	kind := defaultKind
	if kind == "" {
		kind = kindFromPath(path)
	}
	e := SyntaxEntry{NameRu: nameRu, NameEn: nameEn, Kind: kind}
	if t := firstMatch(reTitle, page); t != "" {
		e.Owner, _ = splitRuEn(cleanInline(t))
	}

	secs := chapters(page)
	for name, body := range boldSections(page) {
		if _, ok := secs[name]; !ok {
			secs[name] = body
		}
	}
	for name, body := range secs {
		switch name {
		case "Синтаксис":
			e.Signature = cleanText(body)
		case "Параметры":
			e.Params = cleanText(body)
		case "Возвращаемое значение":
			e.Returns = cleanText(body)
		case "Описание":
			e.Description = cleanText(body)
		case "Пример":
			e.Example = cleanText(body)
		case "Доступность":
			e.Availability = cleanText(body)
		case "Использование в версии":
			e.Since = cleanText(body)
		}
	}

	// Simple pages (e.g. query functions) carry only a description paragraph.
	if e.Signature == "" && e.Params == "" && e.Description == "" {
		e.Description = bodyText(page)
	}
	return e, true
}

// chapters splits the page into V8SH_chapter sections: chapter title (without
// trailing colon) -> raw HTML content until the next chapter or <HR>.
func chapters(page string) map[string]string {
	return splitSections(page, reChapter)
}

// boldSections splits sections marked by <b>Title:</b> (used by shlang/shquery).
func boldSections(page string) map[string]string {
	return splitSections(page, reBold)
}

func splitSections(page string, re *regexp.Regexp) map[string]string {
	locs := re.FindAllStringSubmatchIndex(page, -1)
	out := make(map[string]string, len(locs))
	for i, loc := range locs {
		title := strings.TrimRight(strings.TrimSpace(cleanInline(page[loc[2]:loc[3]])), ": ")
		start := loc[1]
		end := len(page)
		if i+1 < len(locs) {
			end = locs[i+1][0]
		}
		body := page[start:end]
		if hr := strings.Index(strings.ToUpper(body), "<HR>"); hr >= 0 {
			body = body[:hr]
		}
		if _, ok := out[title]; !ok {
			out[title] = body
		}
	}
	return out
}

func kindFromPath(path string) string {
	switch {
	case strings.Contains(path, "/methods/"):
		return "method"
	case strings.Contains(path, "/properties/"):
		return "property"
	case strings.Contains(path, "/events/"):
		return "event"
	case strings.Contains(baseName(path), "ctor_"), strings.Contains(path, "/ctor"):
		return "constructor"
	default:
		return "type"
	}
}

func baseName(path string) string {
	if i := strings.LastIndex(path, "/"); i >= 0 {
		return path[i+1:]
	}
	return path
}

// identBase returns the file's base name if it is a clean ASCII identifier
// (the English keyword for shlang/shquery pages), else "".
func identBase(path string) string {
	b := baseName(path)
	b = strings.TrimSuffix(b, ".html")
	b = strings.TrimSuffix(b, ".st")
	if reIdent.MatchString(b) {
		return b
	}
	return ""
}

func firstMatch(re *regexp.Regexp, s string) string {
	if m := re.FindStringSubmatch(s); m != nil {
		return m[1]
	}
	return ""
}

// splitRuEn splits "РусскоеИмя (EnglishName)" into its parts.
func splitRuEn(s string) (ru, en string) {
	s = strings.TrimSpace(s)
	if i := strings.LastIndex(s, "("); i >= 0 && strings.HasSuffix(s, ")") {
		ru = strings.TrimSpace(s[:i])
		en = strings.TrimSpace(strings.TrimSuffix(s[i+1:], ")"))
		return ru, en
	}
	return s, ""
}

// bodyText returns the page text after the first heading, tags stripped.
func bodyText(page string) string {
	s := page
	if i := strings.Index(strings.ToLower(s), "</h1>"); i >= 0 {
		s = s[i+5:]
	}
	if hr := strings.Index(strings.ToUpper(s), "<HR>"); hr >= 0 {
		s = s[:hr]
	}
	return cleanText(s)
}

// cleanInline strips tags and entities and collapses to a single line.
func cleanInline(s string) string {
	s = reTag.ReplaceAllString(s, "")
	s = html.UnescapeString(s)
	s = strings.ReplaceAll(s, "\r", " ")
	s = strings.ReplaceAll(s, "\n", " ")
	return strings.TrimSpace(reSpaces.ReplaceAllString(s, " "))
}

// cleanText strips tags and entities but keeps meaningful line breaks (<br>,
// parameter rubrics), trimming each line and dropping empties.
func cleanText(s string) string {
	s = reBr.ReplaceAllString(s, "\n")
	s = reRubric.ReplaceAllString(s, "\n")
	s = reTag.ReplaceAllString(s, "")
	s = html.UnescapeString(s)

	var lines []string
	for _, ln := range strings.Split(s, "\n") {
		ln = strings.TrimSpace(reSpaces.ReplaceAllString(ln, " "))
		if ln != "" {
			lines = append(lines, ln)
		}
	}
	return strings.Join(lines, "\n")
}
