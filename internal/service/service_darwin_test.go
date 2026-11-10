//go:build darwin

package service

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInstallLaunchAgentEnablesBeforeBootstrapAndRetriesTeardown(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("LaunchAgent installation requires a signed-in user")
	}
	root := t.TempDir()
	bin := filepath.Join(root, "bin")
	if err := os.Mkdir(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(root, "launchctl.log")
	stub := `#!/bin/sh
echo "$1" >> "$LIGHTHOUSE_LAUNCHCTL_LOG"
case "$1" in
  enable) touch "$LIGHTHOUSE_LAUNCHCTL_LOG.enabled" ;;
  bootstrap)
    test -f "$LIGHTHOUSE_LAUNCHCTL_LOG.enabled" || exit 5
    if test ! -f "$LIGHTHOUSE_LAUNCHCTL_LOG.retry"; then
      touch "$LIGHTHOUSE_LAUNCHCTL_LOG.retry"
      exit 5
    fi ;;
esac
`
	if err := os.WriteFile(filepath.Join(bin, "launchctl"), []byte(stub), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", root)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("LIGHTHOUSE_LAUNCHCTL_LOG", log)
	m, err := New("/usr/local/bin/lighthouse", filepath.Join(root, "lighthouse.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Install(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := strings.Fields(string(data)), []string{"enable", "bootout", "bootstrap", "bootstrap", "kickstart"}; strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("launchctl operations = %v, want %v", got, want)
	}
}
