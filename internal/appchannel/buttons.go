package appchannel

import (
	"regexp"
	"strings"
)

var buttonsLineRe = regexp.MustCompile(`^\s*BUTTONS:\s*(.+?)\s*$`)

// MaxButtons caps how many buttons one message shows.
const MaxButtons = 4

// ExtractButtons removes a "BUTTONS: Approve | Not now" line (outside code
// blocks) from an agent reply and returns the labels. The app shows them as
// buttons; tapping one sends its label as a reply to that message. Only the
// last such line counts.
func ExtractButtons(text string) (string, []string) {
	lines := strings.Split(text, "\n")
	inCode := false
	at := -1
	var labels []string
	for i, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			inCode = !inCode
			continue
		}
		if inCode {
			continue
		}
		if m := buttonsLineRe.FindStringSubmatch(line); m != nil {
			var found []string
			for _, l := range strings.Split(m[1], "|") {
				l = strings.Trim(strings.TrimSpace(l), "`*_\"'")
				if l != "" && len([]rune(l)) <= 40 {
					found = append(found, l)
				}
			}
			if len(found) > 0 {
				at, labels = i, found
			}
		}
	}
	if at < 0 {
		return text, []string{}
	}
	if len(labels) > MaxButtons {
		labels = labels[:MaxButtons]
	}
	kept := append(append([]string{}, lines[:at]...), lines[at+1:]...)
	return strings.TrimSpace(strings.Join(kept, "\n")), labels
}
