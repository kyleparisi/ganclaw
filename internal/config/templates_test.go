package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWorkspaceTemplates(t *testing.T) {
	dir := filepath.Join("..", "..", "templates", "workspace")

	t.Run("Has a starter file for every default instruction file", func(t *testing.T) {
		for _, name := range DefaultInstructionFiles {
			data, err := os.ReadFile(filepath.Join(dir, name))
			require.NoError(t, err, name)
			assert.NotEmpty(t, strings.TrimSpace(string(data)), name)
		}
	})

	t.Run("Has no files that are not default instruction files", func(t *testing.T) {
		entries, err := os.ReadDir(dir)
		require.NoError(t, err)
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		assert.ElementsMatch(t, DefaultInstructionFiles, names)
	})
}

func TestReadmeAgentExample(t *testing.T) {
	subject := Load

	t.Run("The config snippet in the README creating-an-agent section is valid", func(t *testing.T) {
		readme, err := os.ReadFile(filepath.Join("..", "..", "README.md"))
		require.NoError(t, err)
		section := string(readme)
		_, section, ok := strings.Cut(section, "## Creating an agent")
		require.True(t, ok)
		section, _, _ = strings.Cut(section, "\n## ")
		_, snippet, ok := strings.Cut(section, "```toml\n")
		require.True(t, ok)
		snippet, _, ok = strings.Cut(snippet, "```")
		require.True(t, ok)

		cfg, err := subject(writeConfig(t, "state_dir = \"/var/lib/ganclaw\"\n"+snippet))

		require.NoError(t, err)
		agent, ok := cfg.Agent("support")
		require.True(t, ok)
		assert.Equal(t, "/home/ganclaw/agents/support", agent.Workspace)
		assert.Equal(t, DefaultInstructionFiles, agent.InstructionFiles)
	})
}
