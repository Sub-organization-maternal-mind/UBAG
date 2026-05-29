import {
  spawnSync
} from "node:child_process";
import {
  DEFAULT_HEALTH_BASE_URLS,
  HEALTH_PROBES,
  evaluateHealthProbeResults,
  runHttpHealthProbes,
  validateHealthProbeRegistry
} from "../src/index.mjs";

const registryErrors = validateHealthProbeRegistry();
if (registryErrors.length > 0) {
  console.error(`Health probe registry validation failed:\n${registryErrors.map((error) => `- ${error}`).join("\n")}`);
  process.exit(1);
}

const requestedIds = new Set(parseList(process.env.UBAG_HEALTH_PROBES));
const includeCommands = process.env.UBAG_HEALTH_INCLUDE_COMMANDS === "1";
const probes = HEALTH_PROBES.filter((probe) => probe.kind === "http" || includeCommands || requestedIds.has(probe.id)).filter((probe) => {
  return requestedIds.size === 0 || requestedIds.has(probe.id);
});

const baseUrls = {
  ...DEFAULT_HEALTH_BASE_URLS,
  gateway: process.env.UBAG_GATEWAY_BASE_URL ?? DEFAULT_HEALTH_BASE_URLS.gateway,
  ingress: process.env.UBAG_INGRESS_BASE_URL ?? DEFAULT_HEALTH_BASE_URLS.ingress,
  prometheus: process.env.UBAG_PROMETHEUS_BASE_URL ?? DEFAULT_HEALTH_BASE_URLS.prometheus,
  grafana: process.env.UBAG_GRAFANA_BASE_URL ?? DEFAULT_HEALTH_BASE_URLS.grafana,
  "nats-monitor": process.env.UBAG_NATS_MONITOR_BASE_URL ?? DEFAULT_HEALTH_BASE_URLS["nats-monitor"],
  "minio-api": process.env.UBAG_MINIO_API_BASE_URL ?? DEFAULT_HEALTH_BASE_URLS["minio-api"]
};

const httpResults = await runHttpHealthProbes({ probes: probes.filter((probe) => probe.kind === "http"), baseUrls });
const commandResults = probes
  .filter((probe) => probe.kind === "command")
  .map(runCommandProbe);
const results = [...httpResults, ...commandResults];
const report = evaluateHealthProbeResults(results);

process.stdout.write(`${JSON.stringify({ ...report, results }, null, 2)}\n`);
if (!report.ok) {
  process.exit(1);
}

function parseList(value) {
  return String(value ?? "")
    .split(",")
    .map((item) => item.trim())
    .filter(Boolean);
}

function runCommandProbe(probe) {
  const startedAt = Date.now();
  const command = process.platform === "win32"
    ? { file: process.env.ComSpec ?? "cmd.exe", args: ["/d", "/s", "/c", probe.command] }
    : { file: "sh", args: ["-lc", probe.command] };
  const result = spawnSync(command.file, command.args, {
    cwd: new URL("../../..", import.meta.url),
    encoding: "utf8",
    windowsHide: true,
    timeout: Number(process.env.UBAG_HEALTH_COMMAND_TIMEOUT_MS ?? 120000)
  });
  const errors = [];
  if (result.error) errors.push(result.error.message);
  if ((result.status ?? 1) !== 0) errors.push(`command exited ${result.status ?? "unknown"}`);
  return Object.freeze({
    id: probe.id,
    tier: probe.tier,
    service: probe.service,
    kind: probe.kind,
    ok: errors.length === 0,
    status: result.status ?? 1,
    durationMs: Date.now() - startedAt,
    errors: Object.freeze(errors)
  });
}
