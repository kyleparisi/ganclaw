package main

import (
	"github.com/kyleparisi/ganclaw/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"os/exec"
	"testing"
)

func TestMachinesAppendix(t *testing.T) {
	subject := machinesAppendix
	t.Run("No configured machines adds no instructions", func(t *testing.T) {
		assert.Empty(t, subject(&config.Config{}))
	})
	t.Run("Describes SSH and browser access together", func(t *testing.T) {
		got := subject(&config.Config{Machines: []config.Machine{{Name: "build_mac", Description: "Mac for development.", Capabilities: []string{"Xcode", "iOS simulator"}, BrowserServer: "chrome_build", SSH: config.MachineSSH{Host: "127.0.0.1", Port: 2222, User: "alex", IdentityFile: "/keys/build key"}}}})
		assert.Contains(t, got, "## build_mac")
		assert.Contains(t, got, "Mac for development.")
		assert.Contains(t, got, "Capabilities: Xcode, iOS simulator.")
		assert.Contains(t, got, "Browser MCP server: chrome_build.")
		assert.Contains(t, got, "-p 2222 -l 'alex' -o IdentitiesOnly=yes -i '/keys/build key' '127.0.0.1'")
		assert.Contains(t, got, "StrictHostKeyChecking=yes")
		assert.Contains(t, got, "If a machine is offline, report it")
	})
	t.Run("SSH aliases preserve the SSH defaults", func(t *testing.T) {
		got := subject(&config.Config{Machines: []config.Machine{{Name: "build_mac", SSH: config.MachineSSH{Host: "build-mac"}}}})
		assert.Contains(t, got, "ConnectTimeout=10 'build-mac'")
		assert.NotContains(t, got, " -p ")
		assert.NotContains(t, got, " -l ")
		assert.NotContains(t, got, " -i ")
	})
	t.Run("Browser only machines do not advertise SSH", func(t *testing.T) {
		got := subject(&config.Config{Machines: []config.Machine{{Name: "browser_mac", BrowserServer: "chrome_build"}}})
		assert.Contains(t, got, "Browser MCP server: chrome_build.")
		assert.NotContains(t, got, "SSH command prefix")
	})
}

func TestShellArgument(t *testing.T) {
	subject := shellArgument
	t.Run("Shell metacharacters remain literal", func(t *testing.T) {
		value := "a'b $(printf unsafe) `printf unsafe`\nspace"
		got, err := exec.Command("sh", "-c", "printf %s "+subject(value)).Output()
		require.NoError(t, err)
		assert.Equal(t, value, string(got))
	})
}
