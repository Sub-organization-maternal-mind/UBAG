package cli

import (
	"context"
	"flag"
	"fmt"
	"strings"
)

// usage is returned when the user provides no subcommand or an unknown one.
const usage = `ubag — UBAG gateway CLI

Usage:
  ubag <command> [subcommand] [flags]

Commands:
  auth login       --base-url <url> --app-secret <secret>
  jobs send        --target <t> --prompt <p> [--command-type <ct>]
  jobs get <id>    [--json]
  jobs list        [--json]
  targets list     [--json]
  cache purge
  plugins list
  plugins verify   <manifest-path>
  plugins install  <path> [--allow-capability cap1,cap2,...]
  doctor
  version
  dashboard
  backup           [--out <dir|s3://...>]
  restore          --from <dir|s3://...>
  migrate          --to <tier> [--dry-run] [--from <tier>]
  db-migrate       [--store sqlite|postgres]
`

// Dispatch is the main entry point for the CLI.  args should be os.Args[1:].
// It returns the output string and an error.
func Dispatch(args []string) (string, error) {
	if len(args) == 0 {
		return usage, nil
	}

	cfg, err := LoadConfig()
	if err != nil {
		return "", fmt.Errorf("loading config: %w", err)
	}
	client := NewClient(cfg.BaseURL, cfg.AppSecret, cfg.APIVersion)

	switch args[0] {
	case "auth":
		return dispatchAuth(client, args[1:])
	case "jobs":
		return dispatchJobs(client, args[1:])
	case "targets":
		return dispatchTargets(client, args[1:])
	case "cache":
		return dispatchCache(client, args[1:])
	case "plugins":
		return dispatchPlugins(args[1:])
	case "doctor":
		return CmdDoctor(client)
	case "version":
		return CmdVersion(client)
	case "dashboard":
		return CmdDashboard(client)
	case "migrate":
		// Tier-level migration: ubag migrate --to <tier> [--dry-run] [--from <tier>]
		// Pass a bufio.Reader for stdin in prod; tests inject their own.
		return CmdMigrate(args[1:], nil)
	case "db-migrate":
		// Schema-only SQL migration runner (formerly "ubag migrate --store ...").
		return DispatchBackup(context.Background(), append([]string{"migrate"}, args[1:]...))
	case "backup", "restore":
		return DispatchBackup(context.Background(), append([]string{args[0]}, args[1:]...))
	default:
		return fmt.Sprintf("unknown command %q\n\n%s", args[0], usage), nil
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// auth
// ─────────────────────────────────────────────────────────────────────────────

func dispatchAuth(client *Client, args []string) (string, error) {
	if len(args) == 0 {
		return authUsage(), nil
	}
	switch args[0] {
	case "login":
		fs := flag.NewFlagSet("auth login", flag.ContinueOnError)
		baseURL := fs.String("base-url", "", "Gateway base URL")
		appSecret := fs.String("app-secret", "", "App secret / API key")
		if err := fs.Parse(args[1:]); err != nil {
			return "", err
		}
		return CmdAuthLogin(client, *baseURL, *appSecret)
	default:
		return fmt.Sprintf("unknown auth subcommand %q\n\n%s", args[0], authUsage()), nil
	}
}

func authUsage() string {
	return strings.TrimSpace(`
auth commands:
  auth login  --base-url <url> --app-secret <secret>
`) + "\n"
}

// ─────────────────────────────────────────────────────────────────────────────
// jobs
// ─────────────────────────────────────────────────────────────────────────────

func dispatchJobs(client *Client, args []string) (string, error) {
	if len(args) == 0 {
		return jobsUsage(), nil
	}
	switch args[0] {
	case "send":
		fs := flag.NewFlagSet("jobs send", flag.ContinueOnError)
		target := fs.String("target", "", "Target name")
		prompt := fs.String("prompt", "", "Prompt text")
		cmdType := fs.String("command-type", "", "Command type")
		if err := fs.Parse(args[1:]); err != nil {
			return "", err
		}
		if *target == "" || *prompt == "" {
			return "", fmt.Errorf("--target and --prompt are required")
		}
		return CmdJobsSend(client, *target, *prompt, *cmdType)
	case "get":
		fs := flag.NewFlagSet("jobs get", flag.ContinueOnError)
		jsonOut := fs.Bool("json", false, "Output as JSON")
		if err := fs.Parse(args[1:]); err != nil {
			return "", err
		}
		rest := fs.Args()
		if len(rest) == 0 {
			return "", fmt.Errorf("jobs get requires a job ID")
		}
		return CmdJobsGet(client, rest[0], *jsonOut)
	case "list":
		fs := flag.NewFlagSet("jobs list", flag.ContinueOnError)
		jsonOut := fs.Bool("json", false, "Output as JSON")
		if err := fs.Parse(args[1:]); err != nil {
			return "", err
		}
		return CmdJobsList(client, *jsonOut)
	default:
		return fmt.Sprintf("unknown jobs subcommand %q\n\n%s", args[0], jobsUsage()), nil
	}
}

func jobsUsage() string {
	return strings.TrimSpace(`
jobs commands:
  jobs send   --target <t> --prompt <p> [--command-type <ct>]
  jobs get    <id> [--json]
  jobs list   [--json]
`) + "\n"
}

// ─────────────────────────────────────────────────────────────────────────────
// targets
// ─────────────────────────────────────────────────────────────────────────────

func dispatchTargets(client *Client, args []string) (string, error) {
	if len(args) == 0 {
		return targetsUsage(), nil
	}
	switch args[0] {
	case "list":
		fs := flag.NewFlagSet("targets list", flag.ContinueOnError)
		jsonOut := fs.Bool("json", false, "Output as JSON")
		if err := fs.Parse(args[1:]); err != nil {
			return "", err
		}
		return CmdTargetsList(client, *jsonOut)
	default:
		return fmt.Sprintf("unknown targets subcommand %q\n\n%s", args[0], targetsUsage()), nil
	}
}

func targetsUsage() string {
	return strings.TrimSpace(`
targets commands:
  targets list  [--json]
`) + "\n"
}

// ─────────────────────────────────────────────────────────────────────────────
// cache
// ─────────────────────────────────────────────────────────────────────────────

func dispatchCache(client *Client, args []string) (string, error) {
	if len(args) == 0 {
		return cacheUsage(), nil
	}
	switch args[0] {
	case "purge":
		return CmdCachePurge(client)
	default:
		return fmt.Sprintf("unknown cache subcommand %q\n\n%s", args[0], cacheUsage()), nil
	}
}

func cacheUsage() string {
	return strings.TrimSpace(`
cache commands:
  cache purge
`) + "\n"
}

// ─────────────────────────────────────────────────────────────────────────────
// plugins
// ─────────────────────────────────────────────────────────────────────────────

func dispatchPlugins(args []string) (string, error) {
	if len(args) == 0 {
		return pluginsUsage(), nil
	}
	switch args[0] {
	case "list":
		return CmdPluginsList()
	case "verify":
		if len(args) < 2 {
			return "", fmt.Errorf("plugins verify requires a manifest path")
		}
		return CmdPluginsVerify(args[1])
	case "install":
		if len(args) < 2 {
			return "", fmt.Errorf("plugins install requires a source path or URL")
		}
		fs := flag.NewFlagSet("plugins install", flag.ContinueOnError)
		allowCaps := fs.String("allow-capability", "", "Comma-separated list of allowed capabilities (default: all)")
		if err := fs.Parse(args[2:]); err != nil {
			return "", err
		}
		var caps []string
		if *allowCaps != "" {
			caps = strings.Split(*allowCaps, ",")
			for i := range caps {
				caps[i] = strings.TrimSpace(caps[i])
			}
		}
		return CmdPluginsInstall(args[1], caps)
	default:
		return fmt.Sprintf("unknown plugins subcommand %q\n\n%s", args[0], pluginsUsage()), nil
	}
}

func pluginsUsage() string {
	return strings.TrimSpace(`
plugins commands:
  plugins list
  plugins verify   <manifest-path>
  plugins install  <path> [--allow-capability cap1,cap2,...]
`) + "\n"
}
