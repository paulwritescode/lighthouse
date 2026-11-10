package service

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const (
	launchdLabel = "com.ovenplatform.lighthouse"
	systemdUnit  = "lighthouse.service"
	windowsTask  = "Lighthouse Dashboard"
)

type Manager struct {
	executable string
	database   string
}

func New(executable, database string) (*Manager, error) {
	if executable == "" {
		var err error
		executable, err = os.Executable()
		if err != nil {
			return nil, fmt.Errorf("find Lighthouse executable: %w", err)
		}
	}
	if resolved, err := filepath.EvalSymlinks(executable); err == nil {
		executable = resolved
	}
	if database == "" {
		return nil, errors.New("database path is required")
	}
	if absolute, err := filepath.Abs(database); err == nil {
		database = absolute
	}
	return &Manager{executable: executable, database: database}, nil
}

func (m *Manager) Installed() bool {
	switch runtime.GOOS {
	case "darwin":
		path, err := m.launchAgentPath()
		return err == nil && fileExists(path)
	case "linux":
		path, err := m.systemdUnitPath()
		return err == nil && fileExists(path)
	case "windows":
		return runQuiet("schtasks.exe", "/Query", "/TN", windowsTask) == nil
	default:
		return false
	}
}

func (m *Manager) Install() error {
	switch runtime.GOOS {
	case "darwin":
		return m.installLaunchAgent()
	case "linux":
		return m.installSystemdUnit()
	case "windows":
		return m.installLogonTask()
	default:
		return fmt.Errorf("startup services are not supported on %s", runtime.GOOS)
	}
}

func (m *Manager) Remove() error {
	switch runtime.GOOS {
	case "darwin":
		return m.removeLaunchAgent()
	case "linux":
		return m.removeSystemdUnit()
	case "windows":
		return m.removeLogonTask()
	default:
		return fmt.Errorf("startup services are not supported on %s", runtime.GOOS)
	}
}

func (m *Manager) Start() error {
	switch runtime.GOOS {
	case "darwin":
		return m.launchctl("kickstart", "-k", m.launchdTarget())
	case "linux":
		return m.systemctl("start", systemdUnit)
	case "windows":
		return run("schtasks.exe", "/Run", "/TN", windowsTask).Run()
	default:
		return fmt.Errorf("service controls are not supported on %s", runtime.GOOS)
	}
}

func (m *Manager) Stop() error {
	switch runtime.GOOS {
	case "darwin":
		return m.launchctl("kill", "SIGTERM", m.launchdTarget())
	case "linux":
		return m.systemctl("stop", systemdUnit)
	case "windows":
		return run("schtasks.exe", "/End", "/TN", windowsTask).Run()
	default:
		return fmt.Errorf("service controls are not supported on %s", runtime.GOOS)
	}
}

func (m *Manager) Restart() error {
	switch runtime.GOOS {
	case "darwin":
		return m.launchctl("kickstart", "-k", m.launchdTarget())
	case "linux":
		return m.systemctl("restart", systemdUnit)
	case "windows":
		_ = runQuiet("schtasks.exe", "/End", "/TN", windowsTask)
		return run("schtasks.exe", "/Run", "/TN", windowsTask).Run()
	default:
		return fmt.Errorf("service controls are not supported on %s", runtime.GOOS)
	}
}

func (m *Manager) ForceStop() error {
	switch runtime.GOOS {
	case "darwin":
		return m.launchctl("kill", "SIGKILL", m.launchdTarget())
	case "linux":
		// systemd escalates a stop to SIGKILL after TimeoutStopSec in the unit.
		return m.systemctl("stop", systemdUnit)
	case "windows":
		// schtasks /End terminates only the program owned by this task.
		return run("schtasks.exe", "/End", "/TN", windowsTask).Run()
	default:
		return fmt.Errorf("service controls are not supported on %s", runtime.GOOS)
	}
}

func (m *Manager) launchAgentPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "Library", "LaunchAgents", launchdLabel+".plist"), nil
}

func (m *Manager) launchdTarget() string {
	uid := ""
	if current, err := user.Current(); err == nil {
		uid = current.Uid
	}
	return fmt.Sprintf("gui/%s/%s", uid, launchdLabel)
}

func (m *Manager) launchdDomain() string {
	uid := ""
	if current, err := user.Current(); err == nil {
		uid = current.Uid
	}
	return "gui/" + uid
}

func (m *Manager) installLaunchAgent() error {
	if os.Geteuid() == 0 {
		return errors.New("install the Lighthouse user service without sudo; launchd must run it in your signed-in session")
	}
	path, err := m.launchAgentPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	logDir := filepath.Join(filepath.Dir(filepath.Dir(path)), "Logs")
	if err := os.MkdirAll(logDir, 0o700); err != nil {
		return err
	}
	content := launchAgentContent(m.executable, m.database, logDir)
	if err := atomicWrite(path, []byte(content), 0o600); err != nil {
		return err
	}
	if err := m.launchctl("enable", m.launchdTarget()); err != nil {
		return err
	}
	_ = runQuiet("launchctl", "bootout", m.launchdTarget())
	if err := m.bootstrapLaunchAgent(path); err != nil {
		return err
	}
	return m.Start()
}

func (m *Manager) bootstrapLaunchAgent(path string) error {
	var lastErr error
	for attempt := 0; attempt < 6; attempt++ {
		output, err := exec.Command("launchctl", "bootstrap", m.launchdDomain(), path).CombinedOutput()
		if err == nil {
			return nil
		}
		lastErr = fmt.Errorf("launchctl bootstrap: %w: %s", err, strings.TrimSpace(string(output)))
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) || exitErr.ExitCode() != 5 {
			return lastErr
		}
		if attempt < 5 {
			time.Sleep(time.Duration(1<<attempt) * 200 * time.Millisecond)
		}
	}
	return fmt.Errorf("load Lighthouse user service after launchd retries: %w", lastErr)
}

func launchAgentContent(executable, database, logDir string) string {
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
  <key>Label</key><string>%s</string>
  <key>ProgramArguments</key><array><string>%s</string><string>serve</string><string>--db</string><string>%s</string></array>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><false/>
  <key>ThrottleInterval</key><integer>5</integer>
  <key>StandardOutPath</key><string>%s</string>
  <key>StandardErrorPath</key><string>%s</string>
</dict></plist>
`, launchdLabel, xmlEscape(executable), xmlEscape(database), xmlEscape(filepath.Join(logDir, "lighthouse.log")), xmlEscape(filepath.Join(logDir, "lighthouse-error.log")))
}

func (m *Manager) removeLaunchAgent() error {
	path, err := m.launchAgentPath()
	if err != nil {
		return err
	}
	_ = runQuiet("launchctl", "bootout", m.launchdTarget())
	_ = runQuiet("launchctl", "disable", m.launchdTarget())
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func (m *Manager) systemdUnitPath() (string, error) {
	config, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(config, "systemd", "user", systemdUnit), nil
}

func (m *Manager) installSystemdUnit() error {
	path, err := m.systemdUnitPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	content := systemdUnitContent(m.executable, m.database)
	if err := atomicWrite(path, []byte(content), 0o600); err != nil {
		return err
	}
	if err := m.systemctl("daemon-reload"); err != nil {
		return err
	}
	if err := m.systemctl("enable", "--now", systemdUnit); err != nil {
		return err
	}
	return nil
}

func systemdUnitContent(executable, database string) string {
	return fmt.Sprintf(`[Unit]
Description=Lighthouse local dashboard
After=default.target

[Service]
Type=simple
ExecStart=%s serve --db %s
Restart=on-failure
RestartSec=2
TimeoutStopSec=3
KillMode=control-group
Environment=LIGHTHOUSE_SERVICE=1

[Install]
WantedBy=default.target
`, systemdQuote(executable), systemdQuote(database))
}

func (m *Manager) removeSystemdUnit() error {
	_ = m.systemctl("disable", "--now", systemdUnit)
	path, err := m.systemdUnitPath()
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return m.systemctl("daemon-reload")
}

func (m *Manager) installLogonTask() error {
	command := windowsQuote(m.executable) + " serve --db " + windowsQuote(m.database)
	if err := run("schtasks.exe", "/Create", "/SC", "ONLOGON", "/TN", windowsTask, "/TR", command, "/RL", "LIMITED", "/F").Run(); err != nil {
		return fmt.Errorf("create Windows logon task: %w", err)
	}
	return run("schtasks.exe", "/Run", "/TN", windowsTask).Run()
}

func (m *Manager) removeLogonTask() error {
	if err := runQuiet("schtasks.exe", "/End", "/TN", windowsTask); err != nil {
		// It is valid for the task not to be running when removed.
		if runQuiet("schtasks.exe", "/Query", "/TN", windowsTask) != nil {
			return nil
		}
	}
	return run("schtasks.exe", "/Delete", "/TN", windowsTask, "/F").Run()
}

func (m *Manager) launchctl(args ...string) error { return run("launchctl", args...).Run() }
func (m *Manager) systemctl(args ...string) error {
	return run("systemctl", append([]string{"--user"}, args...)...).Run()
}

func run(name string, args ...string) *exec.Cmd {
	cmd := exec.Command(name, args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	return cmd
}

func runQuiet(name string, args ...string) error {
	return exec.Command(name, args...).Run()
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}

func atomicWrite(path string, data []byte, mode os.FileMode) error {
	temp, err := os.CreateTemp(filepath.Dir(path), ".lighthouse-*")
	if err != nil {
		return err
	}
	name := temp.Name()
	defer os.Remove(name)
	if err := temp.Chmod(mode); err != nil {
		temp.Close()
		return err
	}
	if _, err := temp.Write(data); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}

func xmlEscape(value string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", "\"", "&quot;", "'", "&apos;")
	return r.Replace(value)
}

func systemdQuote(value string) string {
	r := strings.NewReplacer("\\", "\\\\", "\"", "\\\"", "$", "\\$", "`", "\\`")
	return `"` + r.Replace(value) + `"`
}

func windowsQuote(value string) string {
	if !strings.ContainsAny(value, " \t\n\v\"") {
		return value
	}
	var b strings.Builder
	b.WriteByte('"')
	backslashes := 0
	for _, r := range value {
		if r == '\\' {
			backslashes++
			continue
		}
		if r == '"' {
			b.WriteString(strings.Repeat("\\", backslashes*2+1))
			b.WriteRune(r)
			backslashes = 0
			continue
		}
		b.WriteString(strings.Repeat("\\", backslashes))
		backslashes = 0
		b.WriteRune(r)
	}
	b.WriteString(strings.Repeat("\\", backslashes*2))
	b.WriteByte('"')
	return b.String()
}
