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
	setupWelcome setupStep = iota
	setupHostname
	setupService
	setupComplete
)

type setupModel struct {
	step     setupStep
	selected int
	options  SetupOptions
	width    int
	isDark   bool
	finished bool
}

func RunSetup() (SetupOptions, bool, error) {
	program := tea.NewProgram(setupModel{width: 72, isDark: true})
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
	case tea.WindowSizeMsg:
		m.width = message.Width
	case tea.BackgroundColorMsg:
		m.isDark = message.IsDark()
	case tea.KeyPressMsg:
		switch message.String() {
		case "ctrl+c", "esc", "q":
			return m, tea.Quit
		case "left", "h", "y":
			if m.step == setupHostname || m.step == setupService {
				m.selected = 0
			}
		case "right", "l", "n":
			if m.step == setupHostname || m.step == setupService {
				m.selected = 1
			}
		case "enter", " ":
			switch m.step {
			case setupWelcome:
				m.step = setupHostname
				m.selected = 1
			case setupHostname:
				m.options.RegisterHostname = m.selected == 0
				m.step = setupService
				m.selected = 1
			case setupService:
				m.options.InstallService = m.selected == 0
				m.step = setupComplete
			case setupComplete:
				m.finished = true
				return m, tea.Quit
			}
		}
	}
	return m, nil
}

func (m setupModel) View() tea.View {
	width := m.width
	if width < 46 {
		width = 46
	}
	if width > 76 {
		width = 76
	}
	frame := lipgloss.NewStyle().Padding(1, 2).Width(width)
	line := lipgloss.NewStyle().Foreground(adaptiveColor(m.isDark, "#D8D5D1", "#747780")).Render(strings.Repeat("─", width-4))
	detailStyle := lipgloss.NewStyle().Foreground(adaptiveColor(m.isDark, "#71605B", "#A9A3A0"))
	headingStyle := lipgloss.NewStyle().Foreground(adaptiveColor(m.isDark, "#252525", "#F4F2ED")).Bold(true)
	var body strings.Builder
	body.WriteString(headingStyle.Render("◆  LIGHTHOUSE") + "  " + detailStyle.Render("First-time setup") + "\n")
	body.WriteString(line + "\n\n")
	switch m.step {
	case setupWelcome:
		body.WriteString(headingStyle.Render("Let's set up Lighthouse.") + "\n\n")
		body.WriteString("I'll ask one question at a time. Your database stays on this device.\n")
		body.WriteString("You can change these choices later from the Lighthouse menu.\n\n")
		body.WriteString(detailStyle.Render("Press enter to begin · q to quit"))
	case setupHostname:
		body.WriteString(detailStyle.Render("STEP 1 OF 2") + "\n\n")
		body.WriteString(headingStyle.Render("Use the local address lighthouse?") + "\n\n")
		body.WriteString("This adds a local hosts-file entry so you can use\nhttp://lighthouse:8080 instead of http://localhost:8080.\n")
		body.WriteString("Choosing Yes will ask your operating system for permission.\n\n")
		body.WriteString(setupChoices(m.selected) + "\n\n")
		body.WriteString(detailStyle.Render("← → choose · enter continue · q quit"))
	case setupService:
		body.WriteString(detailStyle.Render("STEP 2 OF 2") + "\n\n")
		body.WriteString(headingStyle.Render("Start Lighthouse when you sign in?") + "\n\n")
		body.WriteString("This installs a per-user background service and starts it now.\n")
		body.WriteString("You can stop, restart, or remove it later from the menu.\n\n")
		body.WriteString(setupChoices(m.selected) + "\n\n")
		body.WriteString(detailStyle.Render("← → choose · enter continue · q quit"))
	case setupComplete:
		body.WriteString(headingStyle.Render("Your local setup is ready.") + "\n\n")
		body.WriteString("The Lighthouse menu will open next. Start the dashboard there\nwhenever you want to use it.\n\n")
		body.WriteString(detailStyle.Render("Press enter to continue"))
	}
	body.WriteString("\n" + line)
	return tea.NewView(frame.Render(body.String()))
}

func setupChoices(selected int) string {
	yes, no := "  Yes  ", "  No  "
	if selected == 0 {
		yes = selectedStyle.Render("  Yes  ")
	} else {
		no = selectedStyle.Render("  No  ")
	}
	return yes + "   " + no
}
