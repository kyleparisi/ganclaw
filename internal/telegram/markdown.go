package telegram

import (
	"regexp"
	"strconv"
	"strings"
)

// RenderMarkdown converts the Markdown agents write into the HTML subset
// Telegram understands (parse_mode=HTML): bold, italic, strikethrough,
// inline code, code blocks, links and quotes. Headings become bold lines,
// bullets become "•" and tables become monospace blocks, since Telegram
// has none of those. Anything else is shown as written, escaped.
func RenderMarkdown(s string) string {
	lines := strings.Split(s, "\n")
	var out []string
	for i := 0; i < len(lines); i++ {
		trimmed := strings.TrimSpace(lines[i])
		switch {
		case isFence(trimmed):
			// An unclosed fence runs to the end, as it does while a reply
			// is still streaming.
			fence, lang := fenceOf(trimmed)
			var code []string
			for i++; i < len(lines) && !closesFence(strings.TrimSpace(lines[i]), fence); i++ {
				code = append(code, lines[i])
			}
			out = append(out, codeBlock(strings.Join(code, "\n"), lang))
		case strings.HasPrefix(trimmed, "|"):
			var rows []string
			for ; i < len(lines) && strings.HasPrefix(strings.TrimSpace(lines[i]), "|"); i++ {
				rows = append(rows, strings.TrimSpace(lines[i]))
			}
			i--
			out = append(out, codeBlock(strings.Join(rows, "\n"), ""))
		case strings.HasPrefix(trimmed, ">"):
			var quoted []string
			for ; i < len(lines) && strings.HasPrefix(strings.TrimSpace(lines[i]), ">"); i++ {
				q := strings.TrimPrefix(strings.TrimSpace(lines[i]), ">")
				quoted = append(quoted, renderInline(strings.TrimPrefix(q, " ")))
			}
			i--
			out = append(out, "<blockquote>"+strings.Join(quoted, "\n")+"</blockquote>")
		default:
			out = append(out, renderLine(lines[i]))
		}
	}
	return strings.Join(out, "\n")
}

var (
	headingRe = regexp.MustCompile(`^\s{0,3}#{1,6}\s+(.*?)(\s+#+)?\s*$`)
	bulletRe  = regexp.MustCompile(`^(\s*)[-*+]\s+(.*)$`)
	ruleRe    = regexp.MustCompile(`^\s{0,3}([-*_])(\s*[-*_]){2,}\s*$`)
)

func renderLine(line string) string {
	if m := headingRe.FindStringSubmatch(line); m != nil {
		return "<b>" + renderInline(m[1]) + "</b>"
	}
	if ruleRe.MatchString(line) {
		return "──────────"
	}
	if m := bulletRe.FindStringSubmatch(line); m != nil {
		return m[1] + "• " + renderInline(m[2])
	}
	return renderInline(line)
}

func isFence(line string) bool {
	if strings.HasPrefix(line, "~~~") {
		return true
	}
	// ```code``` on one line is inline code, not a fence.
	return strings.HasPrefix(line, "```") && !strings.Contains(strings.TrimLeft(line, "`"), "`")
}

// fenceOf returns the fence characters ("```", "````", "~~~", …) and the
// language named after them.
func fenceOf(line string) (fence, lang string) {
	c := line[:1]
	rest := strings.TrimLeft(line, c)
	fence = line[:len(line)-len(rest)]
	if f := strings.Fields(rest); len(f) > 0 {
		lang = f[0]
	}
	return fence, lang
}

func closesFence(line, fence string) bool {
	return strings.HasPrefix(line, fence) && strings.Trim(line, fence[:1]) == ""
}

var (
	textEscaper = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")
	attrEscaper = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;")
)

// escape escapes the characters Telegram's HTML parser needs escaped.
func escape(s string) string { return textEscaper.Replace(s) }

func codeBlock(code, lang string) string {
	if lang != "" {
		return `<pre><code class="language-` + attrEscaper.Replace(lang) + `">` + escape(code) + "</code></pre>"
	}
	return "<pre>" + escape(code) + "</pre>"
}

var (
	codeSpanRe    = regexp.MustCompile("(`+)(.+?)(`+)")
	linkRe        = regexp.MustCompile(`\[([^\]\n]+)\]\(((?:https?|tg|mailto):[^)\s]+)\)`)
	boldRe        = regexp.MustCompile(`\*\*(\S(?:.*?\S)?)\*\*`)
	boldUnderRe   = regexp.MustCompile(`(^|[^\pL\pN_])__(\S(?:.*?\S)?)__($|[^\pL\pN_])`)
	strikeRe      = regexp.MustCompile(`~~(\S(?:.*?\S)?)~~`)
	italicRe      = regexp.MustCompile(`(^|[^\pL\pN*])\*(\S(?:[^*]*?\S)?)\*($|[^\pL\pN*])`)
	italicUnderRe = regexp.MustCompile(`(^|[^\pL\pN_])_(\S(?:[^_]*?\S)?)_($|[^\pL\pN_])`)
	placeholderRe = regexp.MustCompile("\x00([0-9]+)\x00")
)

// renderInline renders one line's inline markup. Code spans and link URLs
// are set aside first so emphasis markers inside them are left alone.
func renderInline(s string) string {
	var held []string
	hold := func(h string) string {
		held = append(held, h)
		return "\x00" + strconv.Itoa(len(held)-1) + "\x00"
	}
	s = strings.ReplaceAll(s, "\x00", "")

	s = codeSpanRe.ReplaceAllStringFunc(s, func(m string) string {
		p := codeSpanRe.FindStringSubmatch(m)
		if len(p[1]) != len(p[3]) {
			return m
		}
		return hold("<code>" + escape(strings.TrimSpace(p[2])) + "</code>")
	})
	s = linkRe.ReplaceAllStringFunc(s, func(m string) string {
		p := linkRe.FindStringSubmatch(m)
		return hold(`<a href="` + attrEscaper.Replace(p[2]) + `">` + emphasis(escape(p[1])) + "</a>")
	})
	s = emphasis(escape(s))
	return placeholderRe.ReplaceAllStringFunc(s, func(m string) string {
		n, _ := strconv.Atoi(placeholderRe.FindStringSubmatch(m)[1])
		return held[n]
	})
}

// emphasis applies bold, strikethrough and italic to escaped text.
func emphasis(s string) string {
	s = boldRe.ReplaceAllString(s, "<b>$1</b>")
	s = repeat(s, boldUnderRe, "$1<b>$2</b>$3")
	s = strikeRe.ReplaceAllString(s, "<s>$1</s>")
	s = repeat(s, italicRe, "$1<i>$2</i>$3")
	s = repeat(s, italicUnderRe, "$1<i>$2</i>$3")
	return s
}

// repeat applies a replacement whose match consumes a boundary character
// until nothing changes, so adjacent matches ("*a* *b*") both apply.
func repeat(s string, re *regexp.Regexp, repl string) string {
	for range 5 {
		next := re.ReplaceAllString(s, repl)
		if next == s {
			break
		}
		s = next
	}
	return s
}

// SplitMarkdown splits s like SplitText, but keeps code blocks intact
// across parts: a part that ends inside a fence is closed, and the next
// part reopens it.
func SplitMarkdown(s string, max int) []string {
	parts := SplitText(s, max)
	var open, lang string
	for i, p := range parts {
		if open != "" {
			// The reopened fence is the first line; scanning sees it open.
			p = open + lang + "\n" + p
			open, lang = "", ""
		}
		for _, line := range strings.Split(p, "\n") {
			t := strings.TrimSpace(line)
			switch {
			case open == "" && isFence(t):
				open, lang = fenceOf(t)
			case open != "" && closesFence(t, open):
				open, lang = "", ""
			}
		}
		if open != "" {
			p = strings.TrimRight(p, "\n") + "\n" + open
		}
		parts[i] = p
	}
	return parts
}
