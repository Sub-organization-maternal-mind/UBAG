package tui

import (
	"context"
	"fmt"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/ubag/ubag/apps/gateway/internal/cli"
)

// tab represents a TUI tab.
type tab int

const (
	tabJobs    tab = iota
	tabTargets tab = iota
	tabCache   tab = iota
	tabHealth  tab = iota
)

var tabNames = []string{"Jobs", "Targets", "Cache", "Health"}

// Message types for async data loading via tea.Cmd.
type jobsLoadedMsg struct{ jobs []cli.JobResponse }
type targetsLoadedMsg struct{ targets []cli.TargetResponse }
type healthLoadedMsg struct{ health cli.HealthResponse }
type errorMsg struct{ err error }

// Model is the Bubble Tea model for the ubag TUI.
type Model struct {
	client    *cli.Client
	activeTab tab
	jobs      []cli.JobResponse
	targets   []cli.TargetResponse
	health    cli.HealthResponse
	loading   bool
	lastErr   string
	width     int
	height    int
}

// New creates a new Model with the given client.
func New(client *cli.Client) Model {
	return Model{
		client:  client,
		loading: true,
	}
}

// Init issues the initial data-loading commands.
func (m Model) Init() tea.Cmd {
	return tea.Batch(
		m.loadJobs(),
		m.loadTargets(),
		m.loadHealth(),
	)
}

// Update handles messages and key input. Pure function of (msg, model).
// All I/O is done via tea.Cmd — no direct I/O in this function.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "q", "ctrl+c":
			return m, tea.Quit
		case "tab", "right":
			m.activeTab = tab((int(m.activeTab) + 1) % len(tabNames))
			return m, nil
		case "shift+tab", "left":
			m.activeTab = tab((int(m.activeTab) + len(tabNames) - 1) % len(tabNames))
			return m, nil
		case "r":
			m.loading = true
			return m, tea.Batch(m.loadJobs(), m.loadTargets(), m.loadHealth())
		}
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
	case jobsLoadedMsg:
		m.jobs = msg.jobs
		m.loading = false
	case targetsLoadedMsg:
		m.targets = msg.targets
	case healthLoadedMsg:
		m.health = msg.health
	case errorMsg:
		m.lastErr = msg.err.Error()
		m.loading = false
	}
	return m, nil
}

// View renders the TUI to a string. Pure function.
func (m Model) View() string {
	// Tab bar
	tabBar := renderTabBar(m.activeTab)

	// Status bar
	status := ""
	if m.loading {
		status = "Loading..."
	} else if m.lastErr != "" {
		status = "Error: " + m.lastErr
	} else {
		status = "Ready — [tab] next tab  [r] refresh  [q] quit"
	}

	// Content area
	content := m.renderContent()

	return tabBar + "\n" + content + "\n" + status
}

func renderTabBar(active tab) string {
	activeStyle := lipgloss.NewStyle().Bold(true).Underline(true)
	inactiveStyle := lipgloss.NewStyle()
	bar := ""
	for i, name := range tabNames {
		if tab(i) == active {
			bar += activeStyle.Render("["+name+"]") + "  "
		} else {
			bar += inactiveStyle.Render(" "+name+" ") + "  "
		}
	}
	return bar
}

func (m Model) renderContent() string {
	switch m.activeTab {
	case tabJobs:
		if len(m.jobs) == 0 {
			return "No jobs."
		}
		out := ""
		for _, j := range m.jobs {
			out += fmt.Sprintf("  %-20s  %-16s  %s\n", j.ID, j.Status, j.Target)
		}
		return out
	case tabTargets:
		if len(m.targets) == 0 {
			return "No targets."
		}
		out := ""
		for _, t := range m.targets {
			out += fmt.Sprintf("  %-20s  %s\n", t.Name, t.Kind)
		}
		return out
	case tabCache:
		return "Cache — use [cache purge] CLI command to invalidate."
	case tabHealth:
		if m.health.Status == "" {
			return "Health: unknown"
		}
		return fmt.Sprintf("Status: %s  Version: %s", m.health.Status, m.health.Version)
	default:
		return ""
	}
}

// loadJobs returns a tea.Cmd that fetches jobs and emits a jobsLoadedMsg.
func (m Model) loadJobs() tea.Cmd {
	return func() tea.Msg {
		jobs, err := m.client.ListJobs(context.Background())
		if err != nil {
			return errorMsg{err}
		}
		return jobsLoadedMsg{jobs}
	}
}

func (m Model) loadTargets() tea.Cmd {
	return func() tea.Msg {
		targets, err := m.client.ListTargets(context.Background())
		if err != nil {
			return errorMsg{err}
		}
		return targetsLoadedMsg{targets}
	}
}

func (m Model) loadHealth() tea.Cmd {
	return func() tea.Msg {
		health, err := m.client.Health(context.Background())
		if err != nil {
			return errorMsg{err}
		}
		return healthLoadedMsg{health}
	}
}
