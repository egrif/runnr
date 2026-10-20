package htmlview

import (
	"html"
	"html/template"
	"regexp"
	"strings"
)

var (
	mdCode = regexp.MustCompile("`([^`]+)`")
	mdBold = regexp.MustCompile(`\*\*([^*]+)\*\*`)
	mdItal = regexp.MustCompile(`(^|[\s(])_([^_]+)_`)
	mdList = regexp.MustCompile(`^\s*(?:[-*]|\d+[.)])\s+(.*)`)
	mdHead = regexp.MustCompile(`^(#{1,6})\s+(.*)`)
)

// markdown renders the Markdown an overview is written in: headings,
// paragraphs, bulleted and numbered lists, code blocks, `code`, **bold**
// and _italics_. Everything is escaped first.
func markdown(src string) template.HTML {
	var out strings.Builder
	inline := func(s string) string {
		s = html.EscapeString(s)
		s = mdCode.ReplaceAllString(s, "<code>$1</code>")
		s = mdBold.ReplaceAllString(s, "<b>$1</b>")
		return mdItal.ReplaceAllString(s, "$1<i>$2</i>")
	}
	var para []string
	list := "" // ul | ol
	flush := func() {
		if len(para) > 0 {
			out.WriteString("<p>" + inline(strings.Join(para, " ")) + "</p>\n")
			para = nil
		}
		if list != "" {
			out.WriteString("</" + list + ">\n")
			list = ""
		}
	}
	lines := strings.Split(strings.ReplaceAll(src, "\r\n", "\n"), "\n")
	for i := 0; i < len(lines); i++ {
		l := lines[i]
		t := strings.TrimSpace(l)
		switch {
		case strings.HasPrefix(t, "```"):
			flush()
			var code []string
			for i++; i < len(lines) && !strings.HasPrefix(strings.TrimSpace(lines[i]), "```"); i++ {
				code = append(code, lines[i])
			}
			out.WriteString("<pre><code>" + html.EscapeString(strings.Join(code, "\n")) + "</code></pre>\n")
		case t == "":
			flush()
		case mdHead.MatchString(t):
			flush()
			m := mdHead.FindStringSubmatch(t)
			lvl := min(len(m[1])+2, 6) // under the page's own headings
			out.WriteString("<h" + string(rune('0'+lvl)) + ">" + inline(m[2]) + "</h" + string(rune('0'+lvl)) + ">\n")
		case mdList.MatchString(l):
			if len(para) > 0 {
				out.WriteString("<p>" + inline(strings.Join(para, " ")) + "</p>\n")
				para = nil
			}
			kind := "ul"
			if c := strings.TrimSpace(l)[0]; c >= '0' && c <= '9' {
				kind = "ol"
			}
			if list != kind {
				if list != "" {
					out.WriteString("</" + list + ">\n")
				}
				out.WriteString("<" + kind + ">\n")
				list = kind
			}
			out.WriteString("<li>" + inline(mdList.FindStringSubmatch(l)[1]) + "</li>\n")
		case list != "" && (strings.HasPrefix(l, " ") || strings.HasPrefix(l, "\t")):
			// A list item continued on the next line.
			s := out.String()
			if k := strings.LastIndex(s, "</li>"); k >= 0 {
				out.Reset()
				out.WriteString(s[:k] + " " + inline(t) + s[k:])
			}
		default:
			if list != "" {
				out.WriteString("</" + list + ">\n")
				list = ""
			}
			para = append(para, t)
		}
	}
	flush()
	return template.HTML(out.String())
}
