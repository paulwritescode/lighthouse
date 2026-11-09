package tui

import (
	"context"
	"fmt"
	"image/color"
	"strings"

	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/paulwritescode/lighthouse/internal/updater"
)

type Action string

const (
	OpenDashboard      Action = "open-dashboard"
	StartDashboard     Action = "start-dashboard"
	StartService       Action = "start-service"
	StopDashboard      Action = "stop-dashboard"
	RestartDashboard   Action = "restart-dashboard"
	ForceStopDashboard Action = "force-stop-dashboard"
	InstallStartup     Action = "install-startup"
	RemoveStartup      Action = "remove-startup"
	SetupLocalName     Action = "setup-local-name"
	RemoveLocalName    Action = "remove-local-name"
	CheckUpdates       Action = "check-updates"
	Update             Action = "update"
	Exit               Action = "exit"
)

type item struct {
	title, detail string
	action        Action
	section       string
	danger        bool
}

var menuItems = []item{
	{section: "DASHBOARD", title: "Open Lighthouse", detail: "Open the running local dashboard in your browser", action: OpenDashboard},
	{section: "DASHBOARD", title: "Start dashboard", detail: "Start it in this terminal; press Ctrl+C to stop", action: StartDashboard},
	{section: "DASHBOARD", title: "Start dashboard service", detail: "Start the installed background service", action: StartService},
	{section: "DASHBOARD", title: "Stop dashboard service", detail: "Stop the installed background service", action: StopDashboard},
	{section: "DASHBOARD", title: "Restart dashboard service", detail: "Restart the installed background service", action: RestartDashboard},
	{section: "DASHBOARD", title: "Force stop dashboard", detail: "Terminate the installed background service", action: ForceStopDashboard, danger: true},
	{section: "STARTUP", title: "Install startup service", detail: "Start Lighthouse when you sign in to this account", action: InstallStartup},
	{section: "STARTUP", title: "Remove startup service", detail: "Stop it and remove its startup entry", action: RemoveStartup, danger: true},
	{section: "LOCAL ADDRESS", title: "Register lighthouse hostname", detail: "Add lighthouse to the local hosts file; asks for admin permission", action: SetupLocalName},
	{section: "LOCAL ADDRESS", title: "Remove lighthouse hostname", detail: "Remove only Lighthouse's managed hosts-file entries", action: RemoveLocalName, danger: true},
	{section: "UPDATES", title: "Check for updates", detail: "Compare this build with the latest GitHub release", action: CheckUpdates},
	{section: "UPDATES", title: "Update Lighthouse", detail: "Install the latest release and restart its service", action: Update},
}

var (
	brand         = lipgloss.Color("#252525")
	blue          = lipgloss.Color("#88CDF1")
	selectedStyle = lipgloss.NewStyle().Foreground(brand).Background(blue).Bold(true).Padding(0, 1)
	keyStyle      = lipgloss.NewStyle().Foreground(brand).Background(blue).Bold(true).Padding(0, 1)
)

type model struct {
	cursor, width int
	height        int
	chosen        Action
	quitting      bool
	version       string
	latestVersion string
	updateMessage string
	serviceOn     bool
	hostnameOn    bool
	isDark        bool
	checking      bool
	spinner       spinner.Model
}

func Run(version string, serviceInstalled, hostnameRegistered bool) (Action, error) {
	p := tea.NewProgram(model{width: 80, height: 24, version: version, serviceOn: serviceInstalled, hostnameOn: hostnameRegistered, isDark: true, checking: true, spinner: spinner.New(spinner.WithSpinner(spinner.MiniDot)), updateMessage: "Checking GitHub releases…"})
	m, err := p.Run()
	if err != nil {
		return "", err
	}
	result, ok := m.(model)
	if !ok {
		return "", fmt.Errorf("unexpected menu result")
	}
	if result.chosen == "" {
		return Exit, nil
	}
	return result.chosen, nil
}

type releaseCheckMsg struct {
	version string
	err     error
}

func (m model) Init() tea.Cmd {
	checkRelease := func() tea.Msg {
		release, err := updater.Latest(context.Background())
		return releaseCheckMsg{version: release.TagName, err: err}
	}
	return tea.Batch(tea.RequestBackgroundColor, m.spinner.Tick, checkRelease)
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
	case tea.BackgroundColorMsg:
		m.isDark = msg.IsDark()
	case spinner.TickMsg:
		if !m.checking {
			return m, nil
		}
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd
	case releaseCheckMsg:
		m.checking = false
		if msg.err != nil {
			m.updateMessage = "Release check unavailable · press C to retry"
		} else {
			m.latestVersion = msg.version
			if updater.IsNewer(m.version, m.latestVersion) {
				m.updateMessage = "Update available: " + m.latestVersion + " · choose Update Lighthouse"
			} else {
				m.updateMessage = "Latest release " + m.latestVersion + " · this build " + m.version
			}
		}
	case tea.KeyPressMsg:
		switch msg.String() {
		case "q", "esc", "ctrl+c":
			m.quitting = true
			return m, tea.Quit
		case "up", "k":
			if m.cursor > 0 {
				m.cursor--
			}
		case "down", "j":
			if m.cursor < len(menuItems)-1 {
				m.cursor++
			}
		case "home":
			m.cursor = 0
		case "end":
			m.cursor = len(menuItems) - 1
		case "c":
			m.updateMessage = "Checking GitHub releases…"
			m.checking = true
			return m, m.Init()
		case "enter":
			m.chosen = menuItems[m.cursor].action
			return m, tea.Quit
		}
	}
	return m, nil
}

func (m model) View() tea.View {
	width := m.width
	if width < 60 {
		width = 60
	}
	if width > 88 {
		width = 88
	}
	compact := m.height < 28
	logoStyle := lipgloss.NewStyle().Foreground(adaptiveColor(m.isDark, "#252525", "#F4F2ED")).Bold(true)
	sectionStyle := lipgloss.NewStyle().Foreground(adaptiveColor(m.isDark, "#71605B", "#A9A3A0")).Bold(true).MarginTop(1)
	detailStyle := lipgloss.NewStyle().Foreground(adaptiveColor(m.isDark, "#71605B", "#A9A3A0"))
	dangerStyle := lipgloss.NewStyle().Foreground(adaptiveColor(m.isDark, "#B42318", "#F07373")).Bold(true)
	greenStyle := lipgloss.NewStyle().Foreground(adaptiveColor(m.isDark, "#167B4A", "#72D6A0"))
	blueStyle := lipgloss.NewStyle().Foreground(adaptiveColor(m.isDark, "#176A93", "#88CDF1"))
	lineStyle := lipgloss.NewStyle().Foreground(adaptiveColor(m.isDark, "#D8D5D1", "#747780"))
	var b strings.Builder
	b.WriteString(logoStyle.Render("◆  LIGHTHOUSE") + "  " + detailStyle.Render("v"+strings.TrimPrefix(m.version, "v")) + "\n")
	startupState, hostState := "not installed", "not registered"
	if m.serviceOn {
		startupState = "installed"
	}
	if m.hostnameOn {
		hostState = "registered"
	}
	serviceStyle, hostStyle := detailStyle, detailStyle
	if m.serviceOn {
		serviceStyle = greenStyle
	}
	if m.hostnameOn {
		hostStyle = greenStyle
	}
	b.WriteString(detailStyle.Render("Startup service: ") + serviceStyle.Render(startupState) + detailStyle.Render("  ·  Local hostname: ") + hostStyle.Render(hostState) + "\n")
	if m.checking {
		b.WriteString(blueStyle.Render(m.spinner.View()) + " " + detailStyle.Render(m.updateMessage) + "\n")
	} else if strings.HasPrefix(m.updateMessage, "Release check unavailable") {
		b.WriteString(dangerStyle.Render(m.updateMessage) + "\n")
	} else if strings.HasPrefix(m.updateMessage, "Update available") {
		b.WriteString(blueStyle.Render(m.updateMessage) + "\n")
	} else {
		b.WriteString(greenStyle.Render(m.updateMessage) + "\n")
	}
	b.WriteString(lineStyle.Render(strings.Repeat("─", width-4)) + "\n")
	if !compact {
		b.WriteString(lipgloss.NewStyle().Foreground(adaptiveColor(m.isDark, "#252525", "#F4F2ED")).Bold(true).Render("Your local memory, at your command.") + "\n")
		b.WriteString(detailStyle.Render("Choose an action. System changes always ask before they’re applied.") + "\n")
	}
	lastSection := ""
	for i, row := range menuItems {
		if row.section != lastSection {
			section := sectionStyle
			if compact {
				section = sectionStyle.MarginTop(0)
			}
			b.WriteString(section.Render(row.section) + "\n")
			lastSection = row.section
		}
		marker := "  "
		name := row.title
		if row.danger {
			name = dangerStyle.Render(name)
		}
		if i == m.cursor && !compact {
			marker = "› "
			name = selectedStyle.Render(marker + row.title)
		} else {
			name = "  " + name
		}
		b.WriteString(name + "\n")
		if i == m.cursor {
			b.WriteString("    " + detailStyle.Render(row.detail) + "\n")
		}
	}
	b.WriteString("\n" + lineStyle.Render(strings.Repeat("─", width-4)) + "\n")
	b.WriteString(keyStyle.Render("↑ ↓") + " move  " + keyStyle.Render("enter") + " choose  " + keyStyle.Render("c") + " check  " + keyStyle.Render("q") + " quit")
	return tea.NewView(lipgloss.NewStyle().Padding(1, 2).Width(width).Render(b.String()))
}

func adaptiveColor(isDark bool, light, dark string) color.Color {
	return lipgloss.LightDark(isDark)(lipgloss.Color(light), lipgloss.Color(dark))
}
