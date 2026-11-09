package service

import (
	"encoding/xml"
	"io"
	"strings"
	"testing"
)

func TestLaunchAgentContentIsValidAndStartsDashboardAtLogin(t *testing.T) {
	content := launchAgentContent("/Users/test/Lighthouse & Tools/lighthouse", "/Users/test/Library/Data/lighthouse.db", "/Users/test/Library/Logs")
	decoder := xml.NewDecoder(strings.NewReader(content))
	for {
		if _, err := decoder.Token(); err == io.EOF {
			break
		} else if err != nil {
			t.Fatalf("invalid LaunchAgent plist XML: %v", err)
		}
	}
	for _, expected := range []string{"<key>RunAtLoad</key><true/>", "<key>KeepAlive</key><false/>", "Lighthouse &amp; Tools/lighthouse", "<string>serve</string>", "<string>--db</string>"} {
		if !strings.Contains(content, expected) {
			t.Errorf("LaunchAgent is missing %q", expected)
		}
	}
}

func TestSystemdUserUnitRestartsOnFailureAndHasBoundedStop(t *testing.T) {
	content := systemdUnitContent("/home/test/Lighthouse Tools/lighthouse", "/home/test/Library Data/lighthouse.db")
	for _, expected := range []string{"Restart=on-failure", "RestartSec=2", "TimeoutStopSec=3", "WantedBy=default.target", `ExecStart="/home/test/Lighthouse Tools/lighthouse" serve --db "/home/test/Library Data/lighthouse.db"`} {
		if !strings.Contains(content, expected) {
			t.Errorf("systemd user unit is missing %q", expected)
		}
	}
}

func TestWindowsQuotePreservesWhitespaceAndTrailingBackslashes(t *testing.T) {
	if got, want := windowsQuote(`C:\Program Files\Lighthouse\`), `"C:\Program Files\Lighthouse\\"`; got != want {
		t.Fatalf("windowsQuote() = %q, want %q", got, want)
	}
	if got, want := windowsQuote(`C:\Lighthouse\lighthouse.exe`), `C:\Lighthouse\lighthouse.exe`; got != want {
		t.Fatalf("windowsQuote() = %q, want %q", got, want)
	}
}
