package tui

import (
	"strings"
	"testing"
)

func TestMenuFitsStandardTerminalAndKeepsSelectionVisible(t *testing.T) {
	m := model{width: 80, height: 24, version: "v0.4.3", updateMessage: "Latest release v0.4.3"}
	view := m.View().Content
	if lines := strings.Count(view, "\n") + 1; lines > m.height {
		t.Fatalf("menu uses %d lines in a %d-line terminal", lines, m.height)
	}
	if !strings.Contains(view, "› Open Lighthouse") {
		t.Fatal("selected menu item is not marked")
	}
	if strings.Contains(view, "\n\n") {
		t.Fatal("menu has an empty line between sections")
	}
}
