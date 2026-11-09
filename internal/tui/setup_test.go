package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func TestSetupStartsWithFirstQuestionAndAdvancesOneAtATime(t *testing.T) {
	m := setupModel{step: setupHostname, selected: 1, isDark: true}
	initial := m.View().Content
	if !strings.Contains(initial, "Register lighthouse as a local address?") || strings.Contains(initial, "Press enter to begin") {
		t.Fatalf("setup should open on the first question, got %q", initial)
	}

	updated, _ := m.Update(tea.KeyPressMsg{Code: 'y', Text: "y"})
	m = updated.(setupModel)
	if !m.options.RegisterHostname || m.step != setupService || m.finished {
		t.Fatalf("yes should advance to the service question: %+v", m)
	}
	if !strings.Contains(m.View().Content, "Start Lighthouse when you sign in?") {
		t.Fatal("the second question was not shown")
	}

	updated, _ = m.Update(tea.KeyPressMsg{Code: 'n', Text: "n"})
	m = updated.(setupModel)
	if !m.finished || m.options.InstallService {
		t.Fatalf("no should finish setup without a third confirmation: %+v", m)
	}
}

func TestSetupCancelDoesNotFinish(t *testing.T) {
	m := setupModel{step: setupHostname, selected: 1}
	updated, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEsc})
	if updated.(setupModel).finished {
		t.Fatal("cancelled setup must not be marked complete")
	}
}
