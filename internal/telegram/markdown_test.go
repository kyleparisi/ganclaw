package telegram

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRenderMarkdown(t *testing.T) {
	subject := RenderMarkdown

	t.Run("SuccessFlow", func(t *testing.T) {
		in := "**Decision needed**\n" +
			"1. **Server cleanup:** someone with sudo runs `sudo rm -rf ~/all-images/front/dist.before-fa08e341`, then I rerun.\n" +
			"- **SSH keys:** both work. `id_github` authenticates to GitHub."

		assert.Equal(t, "<b>Decision needed</b>\n"+
			"1. <b>Server cleanup:</b> someone with sudo runs <code>sudo rm -rf ~/all-images/front/dist.before-fa08e341</code>, then I rerun.\n"+
			"• <b>SSH keys:</b> both work. <code>id_github</code> authenticates to GitHub.", subject(in))
	})

	t.Run("Plain text is unchanged apart from escaping", func(t *testing.T) {
		assert.Equal(t, "Tom &amp; Jerry: 1 &lt; 2 &gt; 0, it's \"fine\"", subject(`Tom & Jerry: 1 < 2 > 0, it's "fine"`))
	})

	t.Run("Italic, bold and strikethrough", func(t *testing.T) {
		assert.Equal(t, "<i>a</i> <i>b</i> <b>c</b> <b>d</b> <s>e</s> <i>f</i>", subject("*a* *b* **c** __d__ ~~e~~ _f_"))
	})

	t.Run("Underscores and asterisks inside words and paths are left alone", func(t *testing.T) {
		assert.Equal(t, "snake_case_name and /srv/my_app/my_file.txt and 5 * 3 * 2", subject("snake_case_name and /srv/my_app/my_file.txt and 5 * 3 * 2"))
	})

	t.Run("Markup inside code spans is literal and escaped", func(t *testing.T) {
		assert.Equal(t, "run <code>ls *.go &amp;&amp; echo **hi** &lt;x&gt;</code> now", subject("run `ls *.go && echo **hi** <x>` now"))
	})

	t.Run("Links keep their URL intact", func(t *testing.T) {
		assert.Equal(t, `see <a href="https://x.test/a_b_c?q=1&amp;r=2">the <b>docs</b></a>`, subject("see [the **docs**](https://x.test/a_b_c?q=1&r=2)"))
	})

	t.Run("Only web, tg and mailto links become links", func(t *testing.T) {
		assert.Equal(t, "[x](javascript:alert(1)) [Premium]", subject("[x](javascript:alert(1)) [Premium]"))
	})

	t.Run("Fenced code blocks keep their contents and language", func(t *testing.T) {
		in := "Run:\n```sh\necho **not bold** <tag>\n```\nDone."

		assert.Equal(t, "Run:\n<pre><code class=\"language-sh\">echo **not bold** &lt;tag&gt;</code></pre>\nDone.", subject(in))
	})

	t.Run("An unclosed fence runs to the end", func(t *testing.T) {
		assert.Equal(t, "<pre>partial\ncode …</pre>", subject("```\npartial\ncode …"))
	})

	t.Run("Headings become bold lines", func(t *testing.T) {
		assert.Equal(t, "<b>Status</b>\n<b>Next <i>steps</i></b>\n#hashtag", subject("## Status\n### Next *steps* ###\n#hashtag"))
	})

	t.Run("Nested bullets keep their indent", func(t *testing.T) {
		assert.Equal(t, "• one\n  • two\n• three", subject("- one\n  * two\n+ three"))
	})

	t.Run("Quotes become a blockquote", func(t *testing.T) {
		assert.Equal(t, "<blockquote>first <b>line</b>\nsecond</blockquote>\nafter", subject("> first **line**\n> second\nafter"))
	})

	t.Run("Tables become monospace blocks", func(t *testing.T) {
		in := "| a | b |\n|---|---|\n| 1 | <2> |\nafter"

		assert.Equal(t, "<pre>| a | b |\n|---|---|\n| 1 | &lt;2&gt; |</pre>\nafter", subject(in))
	})

	t.Run("Horizontal rules become a line", func(t *testing.T) {
		assert.Equal(t, "a\n──────────\nb", subject("a\n---\nb"))
	})

	t.Run("Unmatched markers are shown as written", func(t *testing.T) {
		assert.Equal(t, "**half bold and `half code", subject("**half bold and `half code"))
	})
}

func TestSplitMarkdown(t *testing.T) {
	subject := SplitMarkdown

	t.Run("Short text is one part", func(t *testing.T) {
		assert.Equal(t, []string{"hello"}, subject("hello", 100))
	})

	t.Run("A code block split across parts is closed and reopened", func(t *testing.T) {
		in := "intro\n```go\n" + strings.Repeat("line\n", 10) + "```\nafter"

		parts := subject(in, 30)

		require.Greater(t, len(parts), 1)
		for i, p := range parts {
			assert.NotContains(t, RenderMarkdown(p), "```", "part %d renders without stray fences", i)
		}
		assert.True(t, strings.HasSuffix(parts[0], "\n```"))
		assert.True(t, strings.HasPrefix(parts[1], "```go\n"))
		assert.True(t, strings.HasSuffix(parts[len(parts)-1], "after"))
	})

	t.Run("Fences without a language reopen correctly", func(t *testing.T) {
		in := "```\n" + strings.Repeat("x\n", 20) + "```\nend"

		parts := subject(in, 16)

		require.Greater(t, len(parts), 2)
		for i, p := range parts {
			r := RenderMarkdown(p)
			assert.NotContains(t, r, "```", "part %d", i)
		}
		assert.Contains(t, RenderMarkdown(parts[1]), "<pre>x")
	})
}
