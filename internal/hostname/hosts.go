package hostname

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

const (
	startMarker = "# BEGIN LIGHTHOUSE MANAGED HOSTS"
	endMarker   = "# END LIGHTHOUSE MANAGED HOSTS"
	localName   = "lighthouse"
)

func FilePath() string {
	if runtime.GOOS == "windows" {
		root := os.Getenv("SystemRoot")
		if root == "" {
			root = `C:\Windows`
		}
		return filepath.Join(root, "System32", "drivers", "etc", "hosts")
	}
	return "/etc/hosts"
}

func Register() error {
	return modify(FilePath(), true)
}

func Remove() error {
	return modify(FilePath(), false)
}

// ApplyRegistered and ApplyRemoved are the elevated entry points used by the CLI.
func ApplyRegistered() error { return modify(FilePath(), true) }
func ApplyRemoved() error    { return modify(FilePath(), false) }

func Registered() (bool, error) {
	content, err := os.ReadFile(FilePath())
	if err != nil {
		return false, err
	}
	return strings.Contains(string(content), startMarker) && strings.Contains(string(content), endMarker), nil
}

func modify(path string, register bool) error {
	content, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read hosts file: %w", err)
	}
	updated, err := replaceManagedBlock(string(content), register)
	if err != nil {
		return err
	}
	if updated == string(content) {
		return nil
	}
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	temp, err := os.CreateTemp(filepath.Dir(path), ".lighthouse-hosts-*")
	if err != nil {
		return elevatedError(err)
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if err := temp.Chmod(info.Mode().Perm()); err != nil {
		temp.Close()
		return err
	}
	if _, err := temp.WriteString(updated); err != nil {
		temp.Close()
		return elevatedError(err)
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tempPath, path); err != nil {
		return elevatedError(err)
	}
	flushNameCache()
	return nil
}

func flushNameCache() {
	switch runtime.GOOS {
	case "darwin":
		_ = exec.Command("dscacheutil", "-flushcache").Run()
		_ = exec.Command("killall", "-HUP", "mDNSResponder").Run()
	case "windows":
		_ = exec.Command("ipconfig.exe", "/flushdns").Run()
	case "linux":
		_ = exec.Command("resolvectl", "flush-caches").Run()
	}
}

func replaceManagedBlock(content string, register bool) (string, error) {
	start := strings.Index(content, startMarker)
	end := strings.Index(content, endMarker)
	if (start >= 0) != (end >= 0) || (start >= 0 && end < start) {
		return "", errors.New("hosts file contains an incomplete Lighthouse block; edit it manually before retrying")
	}
	if start >= 0 {
		end += len(endMarker)
		if end < len(content) && content[end] == '\r' {
			end++
		}
		if end < len(content) && content[end] == '\n' {
			end++
		}
		content = content[:start] + content[end:]
	}
	if !register {
		return content, nil
	}
	if content != "" && !strings.HasSuffix(content, "\n") {
		content += "\n"
	}
	return content + startMarker + "\n127.0.0.1 lighthouse\n" + endMarker + "\n", nil
}

func elevatedError(err error) error {
	if !errors.Is(err, os.ErrPermission) {
		return err
	}
	return fmt.Errorf("updating %s needs administrator permission; run `lighthouse host apply` from an elevated terminal: %w", FilePath(), err)
}

// Elevate runs the hidden apply command using the platform's standard prompt.
func Elevate(remove bool) error {
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	args := []string{"host", "apply"}
	if remove {
		args = []string{"host", "remove-apply"}
	}
	switch runtime.GOOS {
	case "darwin", "linux":
		cmd := exec.Command("sudo", append([]string{executable}, args...)...)
		cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
		return cmd.Run()
	case "windows":
		argumentList := strings.Join(args, " ")
		ps := "$p = Start-Process -FilePath '" + strings.ReplaceAll(executable, "'", "''") + "' -ArgumentList '" + strings.ReplaceAll(argumentList, "'", "''") + "' -Verb RunAs -Wait -PassThru; exit $p.ExitCode"
		cmd := exec.Command("powershell.exe", "-NoProfile", "-Command", ps)
		cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
		return cmd.Run()
	default:
		return fmt.Errorf("host-file registration is not supported on %s", runtime.GOOS)
	}
}
