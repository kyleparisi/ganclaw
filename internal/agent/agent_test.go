package agent

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestInstructions(t *testing.T) {
	t.Run("Concatenates files in order under headings, skipping missing and empty", func(t *testing.T) {
		files := map[string]string{
			"/ws/SOUL.md":     "  Be kind.  \n",
			"/ws/USER.md":     "\n\n",
			"/ws/AGENTS.md":   "Use the tools.",
			"/ws/IGNORED.md":  "not listed",
			"/ws/MEMORY.md":   "Likes pelicans.",
			"/other/SOUL.md":  "wrong workspace",
			"/ws/IDENTITY.md": "",
		}
		subject := Agent{
			Name:             "test",
			Workspace:        "/ws",
			InstructionFiles: []string{"IDENTITY.md", "SOUL.md", "USER.md", "MISSING.md", "AGENTS.md", "MEMORY.md"},
			ReadFile: func(path string) ([]byte, error) {
				s, ok := files[path]
				if !ok {
					return nil, fs.ErrNotExist
				}
				return []byte(s), nil
			},
		}

		got, err := subject.Instructions()

		require.NoError(t, err)
		assert.Equal(t, "# SOUL.md\n\nBe kind.\n\n# AGENTS.md\n\nUse the tools.\n\n# MEMORY.md\n\nLikes pelicans.", got)
	})

	t.Run("Read errors other than missing are returned", func(t *testing.T) {
		subject := Agent{
			Name:             "test",
			Workspace:        "/ws",
			InstructionFiles: []string{"SOUL.md"},
			ReadFile: func(path string) ([]byte, error) {
				return nil, fs.ErrPermission
			},
		}

		_, err := subject.Instructions()

		assert.ErrorIs(t, err, fs.ErrPermission)
		assert.ErrorContains(t, err, "agent test")
	})

	t.Run("Reads real files by default", func(t *testing.T) {
		ws := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(ws, "SOUL.md"), []byte("real file"), 0o600))
		subject := Agent{Name: "test", Workspace: ws, InstructionFiles: []string{"SOUL.md", "NOPE.md"}}

		got, err := subject.Instructions()

		require.NoError(t, err)
		assert.Equal(t, "# SOUL.md\n\nreal file", got)
	})

	t.Run("Appendix follows the files", func(t *testing.T) {
		subject := Agent{Name: "test", Workspace: "/ws", InstructionFiles: []string{"SOUL.md"}, Appendix: "# Tool servers\n\n- browser",
			ReadFile: func(string) ([]byte, error) { return []byte("Be kind."), nil }}

		got, err := subject.Instructions()

		require.NoError(t, err)
		assert.Equal(t, "# SOUL.md\n\nBe kind.\n\n# Tool servers\n\n- browser", got)
	})

	t.Run("No files gives empty instructions", func(t *testing.T) {
		subject := Agent{Name: "test", Workspace: "/ws", ReadFile: func(string) ([]byte, error) {
			return nil, errors.New("must not be called")
		}}

		got, err := subject.Instructions()

		require.NoError(t, err)
		assert.Empty(t, got)
	})
}
