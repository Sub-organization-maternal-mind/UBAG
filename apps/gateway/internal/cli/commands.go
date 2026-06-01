package cli

import (
	"context"
	"fmt"
	"strings"
)

// ─────────────────────────────────────────────────────────────────────────────
// CmdAuthLogin
// ─────────────────────────────────────────────────────────────────────────────

// CmdAuthLogin saves the given baseURL and appSecret to the config file and
// returns a confirmation message.
func CmdAuthLogin(client *Client, baseURL, appSecret string) (string, error) {
	cfg, err := LoadConfig()
	if err != nil {
		return "", fmt.Errorf("loading config: %w", err)
	}
	if baseURL != "" {
		cfg.BaseURL = baseURL
	}
	if appSecret != "" {
		cfg.AppSecret = appSecret
	}
	if err := SaveConfig(cfg); err != nil {
		return "", fmt.Errorf("saving config: %w", err)
	}
	effective := cfg.BaseURL
	if effective == "" {
		effective = DefaultBaseURL
	}
	return "Logged in to " + effective, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// CmdJobsSend
// ─────────────────────────────────────────────────────────────────────────────

// CmdJobsSend creates a job and returns a short confirmation.
func CmdJobsSend(client *Client, target, prompt, cmdType string) (string, error) {
	job, err := client.CreateJob(context.Background(), CreateJobRequest{
		Target:      target,
		Prompt:      prompt,
		CommandType: cmdType,
	})
	if err != nil {
		return "", err
	}
	return "Job created: " + job.ID, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// CmdJobsGet
// ─────────────────────────────────────────────────────────────────────────────

// CmdJobsGet fetches a single job and returns it formatted as a table or JSON.
func CmdJobsGet(client *Client, id string, jsonOut bool) (string, error) {
	job, err := client.GetJob(context.Background(), id)
	if err != nil {
		return "", err
	}
	if jsonOut {
		return FormatJSON(job)
	}
	headers := []string{"ID", "STATUS", "TARGET"}
	rows := [][]string{{job.ID, job.Status, job.Target}}
	return strings.TrimRight(FormatTable(headers, rows), "\n"), nil
}

// ─────────────────────────────────────────────────────────────────────────────
// CmdJobsList
// ─────────────────────────────────────────────────────────────────────────────

// CmdJobsList lists all jobs and returns them formatted as a table or JSON.
func CmdJobsList(client *Client, jsonOut bool) (string, error) {
	jobs, err := client.ListJobs(context.Background())
	if err != nil {
		return "", err
	}
	if jsonOut {
		return FormatJSON(jobs)
	}
	headers := []string{"ID", "STATUS", "TARGET"}
	rows := make([][]string, 0, len(jobs))
	for _, j := range jobs {
		rows = append(rows, []string{j.ID, j.Status, j.Target})
	}
	return strings.TrimRight(FormatTable(headers, rows), "\n"), nil
}

// ─────────────────────────────────────────────────────────────────────────────
// CmdTargetsList
// ─────────────────────────────────────────────────────────────────────────────

// CmdTargetsList lists all targets and returns them formatted as a table or JSON.
func CmdTargetsList(client *Client, jsonOut bool) (string, error) {
	targets, err := client.ListTargets(context.Background())
	if err != nil {
		return "", err
	}
	if jsonOut {
		return FormatJSON(targets)
	}
	headers := []string{"NAME", "KIND"}
	rows := make([][]string, 0, len(targets))
	for _, t := range targets {
		rows = append(rows, []string{t.Name, t.Kind})
	}
	return strings.TrimRight(FormatTable(headers, rows), "\n"), nil
}

// ─────────────────────────────────────────────────────────────────────────────
// CmdCachePurge
// ─────────────────────────────────────────────────────────────────────────────

// CmdCachePurge invalidates the response cache and returns a confirmation.
func CmdCachePurge(client *Client) (string, error) {
	if err := client.PurgeCache(context.Background()); err != nil {
		return "", err
	}
	return "Cache purged", nil
}

// ─────────────────────────────────────────────────────────────────────────────
// CmdDoctor
// ─────────────────────────────────────────────────────────────────────────────

// CmdDoctor performs a health check and returns a human-readable status.
func CmdDoctor(client *Client) (string, error) {
	h, err := client.Health(context.Background())
	if err != nil {
		return "", fmt.Errorf("health check failed: %w", err)
	}
	status := h.Status
	if status == "" {
		status = "unknown"
	}
	version := h.Version
	if version == "" {
		version = "n/a"
	}
	return fmt.Sprintf("Status: %s  Version: %s", status, version), nil
}

// ─────────────────────────────────────────────────────────────────────────────
// CmdVersion
// ─────────────────────────────────────────────────────────────────────────────

// CmdVersion returns version and supported API versions from the server.
func CmdVersion(client *Client) (string, error) {
	v, err := client.Version(context.Background())
	if err != nil {
		return "", err
	}
	apiVers := strings.Join(v.APIVersions, ", ")
	if apiVers == "" {
		apiVers = "n/a"
	}
	return fmt.Sprintf("Version: %s  API-versions: [%s]", v.Version, apiVers), nil
}
