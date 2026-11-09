package tui

import (
	"fmt"
	"time"

	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

type progressResultMsg struct{ err error }

type progressModel struct {
	label   string
	run     func() error
	spinner spinner.Model
	isDark  bool
	done    bool
	err     error
}

// RunProgress shows a compact spinner while a non-interactive setup step runs.
func RunProgress(label string, run func() error) error {
	model := progressModel{
		label:   label,
		run:     run,
		spinner: spinner.New(spinner.WithSpinner(spinner.MiniDot)),
		isDark:  true,
	}
	result, err := tea.NewProgram(model).Run()
	if err != nil {
		return err
	}
	final, ok := result.(progressModel)
	if !ok {
		return fmt.Errorf("unexpected setup progress result")
	}
	return final.err
}

func (m progressModel) Init() tea.Cmd {
	work := func() tea.Msg {
		started := time.Now()
		err := m.run()
		if remaining := 240*time.Millisecond - time.Since(started); remaining > 0 {
			time.Sleep(remaining)
		}
		return progressResultMsg{err: err}
	}
	return tea.Batch(tea.RequestBackgroundColor, m.spinner.Tick, work)
}

func (m progressModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.BackgroundColorMsg:
		m.isDark = msg.IsDark()
	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd
	case progressResultMsg:
		m.done = true
		m.err = msg.err
		return m, tea.Quit
	}
	return m, nil
}

func (m progressModel) View() tea.View {
	if m.done {
		if m.err != nil {
			red := lipgloss.NewStyle().Foreground(adaptiveColor(m.isDark, "#B42318", "#F07373"))
			return tea.NewView(red.Render("✗") + " " + m.label)
		}
		green := lipgloss.NewStyle().Foreground(adaptiveColor(m.isDark, "#167B4A", "#72D6A0"))
		return tea.NewView(green.Render("✓") + " " + m.label)
	}
	blue := lipgloss.NewStyle().Foreground(adaptiveColor(m.isDark, "#176A93", "#88CDF1"))
	return tea.NewView(blue.Render(m.spinner.View()) + " " + m.label)
}
