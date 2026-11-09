package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

type SetupOptions struct {
	RegisterHostname bool
	InstallService   bool
}

type setupStep int

const (
	setupHostname setupStep = iota
	setupService
)

type setupModel struct {
	step     setupStep
	selected int
	options  SetupOptions
	isDark   bool
	finished bool
}

func RunSetup() (SetupOptions, bool, error) {
	program := tea.NewProgram(setupModel{step: setupHostname, selected: 1, isDark: true})
	result, err := program.Run()
	if err != nil {
		return SetupOptions{}, false, err
	}
	model, ok := result.(setupModel)
	if !ok {
		return SetupOptions{}, false, fmt.Errorf("unexpected setup result")
	}
	return model.options, model.finished, nil
}

func (m setupModel) Init() tea.Cmd { return tea.RequestBackgroundColor }

func (m setupModel) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	switch message := message.(type) {
	case tea.BackgroundColorMsg:
		m.isDark = message.IsDark()
	case tea.KeyPressMsg:
		switch message.String() {
		case "ctrl+c", "esc", "q":
			return m, tea.Quit
		case "up", "left", "k", "h":
			m.selected = 0
		case "down", "right", "j", "l", "tab":
			m.selected = 1
		case "y":
			m.selected = 0
			return m.advance()
		case "n":
			m.selected = 1
			return m.advance()
		case "enter", " ":
			return m.advance()
		}
	}
	return m, nil
}

func (m setupModel) advance() (tea.Model, tea.Cmd) {
	if m.step == setupHostname {
		m.options.RegisterHostname = m.selected == 0
		m.step = setupService
		m.selected = 1
		return m, nil
	}
	m.options.InstallService = m.selected == 0
	m.finished = true
	return m, tea.Quit
}

func (m setupModel) View() tea.View {
	blueStyle := lipgloss.NewStyle().Foreground(adaptiveColor(m.isDark, "#176A93", "#88CDF1")).Bold(true)
	greenStyle := lipgloss.NewStyle().Foreground(adaptiveColor(m.isDark, "#167B4A", "#72D6A0")).Bold(true)
	textStyle := lipgloss.NewStyle().Foreground(adaptiveColor(m.isDark, "#252525", "#F4F2ED"))
	mutedStyle := lipgloss.NewStyle().Foreground(adaptiveColor(m.isDark, "#71605B", "#A9A3A0"))

	step := "1/2"
	question := "Register lighthouse as a local address?"
	hint := "http://lighthouse:8080 · requires admin permission"
	if m.step == setupService {
		step = "2/2"
		question = "Start Lighthouse when you sign in?"
		hint = "Adds a user startup service · removable from the menu"
	}

	var body strings.Builder
	body.WriteString(blueStyle.Render("◆ Lighthouse") + "  " + mutedStyle.Render("Setup "+step) + "\n\n")
	body.WriteString(blueStyle.Render("?") + " " + textStyle.Bold(true).Render(question) + "\n")
	body.WriteString("  " + mutedStyle.Render(hint) + "\n\n")
	for i, choice := range []string{"Yes", "No"} {
		if i == m.selected {
			style := blueStyle
			if i == 0 {
				style = greenStyle
			}
			body.WriteString(style.Render("❯ " + choice))
		} else {
			body.WriteString(mutedStyle.Render("  " + choice))
		}
		body.WriteByte('\n')
	}
	body.WriteString("\n" + mutedStyle.Render("↑↓ select · enter confirm · y/n answer · q quit"))
	return tea.NewView(body.String())
}
