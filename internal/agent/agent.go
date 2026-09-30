// Package agent assembles an agent's system prompt from its workspace.
package agent

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/kyleparisi/ganclaw/internal/provider"
)

type Agent struct {
	Name             string
	Workspace        string
	InstructionFiles []string
	// Settings are per-agent provider options.
	Settings provider.Settings
	// Appendix is added after the instruction files (e.g. a description
	// of the tool servers ganclaw provides).
	Appendix string
	// ReadFile defaults to os.ReadFile.
	ReadFile func(path string) ([]byte, error)
}

// Instructions concatenates the agent's instruction files, each under a
// heading with its file name. Missing files are skipped; other read errors
// are returned.
func (a Agent) Instructions() (string, error) {
	read := a.ReadFile
	if read == nil {
		read = os.ReadFile
	}
	var b strings.Builder
	for _, name := range a.InstructionFiles {
		data, err := read(filepath.Join(a.Workspace, name))
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return "", fmt.Errorf("agent %s: %w", a.Name, err)
		}
		text := strings.TrimSpace(string(data))
		if text == "" {
			continue
		}
		if b.Len() > 0 {
			b.WriteString("\n\n")
		}
		fmt.Fprintf(&b, "# %s\n\n%s", name, text)
	}
	if a.Appendix != "" {
		if b.Len() > 0 {
			b.WriteString("\n\n")
		}
		b.WriteString(a.Appendix)
	}
	return b.String(), nil
}
