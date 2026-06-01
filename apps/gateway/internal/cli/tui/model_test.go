package tui

import (
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/ubag/ubag/apps/gateway/internal/cli"
)

func TestUpdate_TabNavigation(t *testing.T) {
	m := New(nil) // client is nil; tests don't invoke commands
	m.loading = false

	// Tab forward
	m1, _ := m.Update(tea.KeyMsg{Type: tea.KeyTab})
	model1 := m1.(Model)
	if model1.activeTab != tabTargets {
		t.Errorf("after tab: activeTab = %v, want tabTargets", model1.activeTab)
	}

	// Tab wraps around
	m2 := model1
	m2.activeTab = tabHealth
	m3, _ := m2.Update(tea.KeyMsg{Type: tea.KeyTab})
	model3 := m3.(Model)
	if model3.activeTab != tabJobs {
		t.Errorf("tab wrap: activeTab = %v, want tabJobs", model3.activeTab)
	}
}

func TestUpdate_JobsLoaded(t *testing.T) {
	m := New(nil)
	jobs := []cli.JobResponse{{ID: "job_001", Status: "completed", Target: "mock"}}
	m2, _ := m.Update(jobsLoadedMsg{jobs: jobs})
	model := m2.(Model)
	if len(model.jobs) != 1 {
		t.Fatalf("jobs len = %d, want 1", len(model.jobs))
	}
	if model.jobs[0].ID != "job_001" {
		t.Errorf("job ID = %q, want job_001", model.jobs[0].ID)
	}
	if model.loading {
		t.Error("loading should be false after jobsLoadedMsg")
	}
}

func TestUpdate_ErrorMsg(t *testing.T) {
	m := New(nil)
	m2, _ := m.Update(errorMsg{err: fmt.Errorf("gateway unavailable")})
	model := m2.(Model)
	if model.lastErr == "" {
		t.Error("lastErr should be set after errorMsg")
	}
	if model.loading {
		t.Error("loading should be false after errorMsg")
	}
}

func TestView_ContainsTabNames(t *testing.T) {
	m := New(nil)
	m.loading = false
	view := m.View()
	for _, name := range tabNames {
		if !strings.Contains(view, name) {
			t.Errorf("View() missing tab name %q", name)
		}
	}
}

func TestView_JobsTab(t *testing.T) {
	m := New(nil)
	m.loading = false
	m.jobs = []cli.JobResponse{{ID: "job_xyz", Status: "running", Target: "browser"}}
	view := m.View()
	if !strings.Contains(view, "job_xyz") {
		t.Errorf("View() on Jobs tab doesn't show job ID")
	}
}

func TestView_HealthTab(t *testing.T) {
	m := New(nil)
	m.loading = false
	m.activeTab = tabHealth
	m.health = cli.HealthResponse{Status: "ok", Version: "1.0.0"}
	view := m.View()
	if !strings.Contains(view, "ok") {
		t.Errorf("View() health tab doesn't show status")
	}
}
