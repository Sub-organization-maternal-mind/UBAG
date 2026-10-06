import { createHmac } from "node:crypto";
import { readFile } from "node:fs/promises";
import { fileURLToPath } from "node:url";
import { dirname, join } from "node:path";
import Ajv2020 from "ajv/dist/2020.js";
import addFormats from "ajv-formats";
import { parse as parseYaml } from "yaml";

const currentDir = dirname(fileURLToPath(import.meta.url));
const fixturePath = join(currentDir, "..", "fixtures", "v0", "scenarios.json");

const fixture = JSON.parse(await readFile(fixturePath, "utf8"));
const errors = [];

if (fixture.suite !== "ubag.v0.sdk.baseline") {
  errors.push("fixture.suite must be ubag.v0.sdk.baseline");
}

if (fixture.api_version !== "2026-05-22") {
  errors.push("fixture.api_version must be 2026-05-22");
}

if (!Array.isArray(fixture.scenarios) || fixture.scenarios.length === 0) {
  errors.push("fixture.scenarios must contain at least one scenario");
}
if (!Array.isArray(fixture.coverage_scenarios) || fixture.coverage_scenarios.length === 0) {
  errors.push("fixture.coverage_scenarios must contain non-REST contract coverage scenarios");
}

const ids = new Set();
const requiredEndpointIds = new Set([
  "health.ok",
  "ready.ok",
  "version.ok",
  "workflows.list.ok",
  "templates.list.ok",
  "targets.list.ok",
  "adapters.list.ok",
  "apps.list.ok",
  "devices.list.ok",
  "audit.list.ok",
  "webhooks.list.ok",
  "events.list.ok",
  "conversations.list.ok",
  "cache.status.ok",
  "metrics.get.ok",
  "jobs.create.accepted",
  "jobs.create.idempotent-replay",
  "jobs.get.completed",
  "jobs.events.list.ok",
  "jobs.events.stream-sse.ok",
  "jobs.artifacts.list.ok",
  "jobs.artifacts.get.ok",
  "jobs.artifacts.put.accepted",
  "jobs.artifacts.delete.accepted",
  "jobs.list.filtered",
  "jobs.cancel.accepted",
  "jobs.retry.accepted",
  "webhooks.replay.accepted",
  "errors.auth-missing",
  "errors.idempotency-conflict",
  "errors.rate-limited"
]);
const requiredCoverageCategories = new Set([
  "retries",
  "streaming",
  "timeouts",
  "unicode",
  "large_payloads",
  "malformed_responses",
  "webhooks",
  "sidecar"
]);

const coverageCategories = new Set();
for (const [index, scenario] of (fixture.coverage_scenarios ?? []).entries()) {
  const prefix = `coverage_scenarios[${index}]`;
  requireString(scenario.id, `${prefix}.id`);
  requireString(scenario.category, `${prefix}.category`);
  requireString(scenario.title, `${prefix}.title`);
  if (!Array.isArray(scenario.assertions) || scenario.assertions.length === 0) {
    errors.push(`${prefix}.assertions must be a non-empty array`);
  }
  coverageCategories.add(scenario.category);
}

for (const [index, scenario] of (fixture.scenarios ?? []).entries()) {
  const prefix = `scenarios[${index}]`;
  if (!scenario || typeof scenario !== "object") {
    errors.push(`${prefix} must be an object`);
    continue;
  }

  requireString(scenario.id, `${prefix}.id`);
  requireString(scenario.category, `${prefix}.category`);
  requireString(scenario.title, `${prefix}.title`);

  if (ids.has(scenario.id)) {
    errors.push(`${prefix}.id duplicates ${scenario.id}`);
  }
  ids.add(scenario.id);

  if (!scenario.request || typeof scenario.request !== "object") {
    errors.push(`${prefix}.request must be an object`);
  } else {
    requireString(scenario.request.method, `${prefix}.request.method`);
    requireString(scenario.request.path, `${prefix}.request.path`);
    if (!String(scenario.request.path).startsWith("/v1/")) {
      errors.push(`${prefix}.request.path must start with /v1/`);
    }
    if (scenario.request.method === "POST" && scenario.request.path === "/v1/jobs") {
      if (!scenario.request.body || typeof scenario.request.body !== "object") {
        errors.push(`${prefix}.request.body is required for POST /v1/jobs`);
      } else {
        const sdk = scenario.request.body.client?.sdk;
        if (sdk?.name !== "__SDK_NAME__" || sdk?.version !== "__SDK_VERSION__") {
          errors.push(`${prefix}.request.body.client.sdk must use SDK placeholders`);
        }
      }
    }
    if (JSON.stringify(scenario.request).includes("ubag-typescript")) {
      errors.push(`${prefix}.request must not hard-code a language-specific SDK name`);
    }
  }

  if (!scenario.response || typeof scenario.response !== "object") {
    errors.push(`${prefix}.response must be an object`);
  } else if (!Number.isInteger(scenario.response.status)) {
    errors.push(`${prefix}.response.status must be an integer`);
  }

  if (!scenario.expect || typeof scenario.expect !== "object") {
    errors.push(`${prefix}.expect must be an object`);
  } else {
    const expectationKeys = Object.keys(scenario.expect);
    if (!expectationKeys.some((key) => key === "ok" || key === "throws" || key.startsWith("body.") || key.startsWith("error."))) {
      errors.push(`${prefix}.expect must include at least one known expectation key`);
    }
  }
}

for (const requiredId of requiredEndpointIds) {
  if (!ids.has(requiredId)) {
    errors.push(`fixture missing required scenario ${requiredId}`);
  }
}
for (const category of requiredCoverageCategories) {
  if (!coverageCategories.has(category)) {
    errors.push(`fixture missing coverage scenario category ${category}`);
  }
}

// Redaction guards for the v2.1 observability scenarios. Browser/alert reads
// must never leak storage-state URIs or SMTP secrets.
const serializedFixture = JSON.stringify(fixture);
if (/"storage_state_uri"/i.test(serializedFixture)) {
  errors.push("fixtures must not expose a storage_state_uri");
}

for (const scenario of fixture.scenarios ?? []) {
  const body = scenario.response?.body;
  if (!body || typeof body !== "object") continue;

  if (scenario.id === "alerts.config.ok") {
    if (/password/i.test(JSON.stringify(body))) {
      errors.push("alerts.config.ok response must not contain a password");
    }
    if (body.smtp_configured === undefined) {
      errors.push("alerts.config.ok must expose an smtp_configured flag");
    }
  }

  if (scenario.id === "browser.contexts.ok" || scenario.id === "browser.tabs.ok") {
    const rows = Array.isArray(body.data) ? body.data : [];
    if (scenario.id === "browser.contexts.ok" && !rows.every((row) => typeof row.has_storage_state === "boolean")) {
      errors.push(`${scenario.id} rows must include a boolean has_storage_state`);
    }
    if (/"storage_state_uri"/i.test(JSON.stringify(rows))) {
      errors.push(`${scenario.id} must not expose a storage_state_uri`);
    }
  }
}

// Voice relay protocol v2 shared vectors (P2.8). The same fixture is asserted
// by the Python relay tests and the Go gateway tests; here we check its shape,
// recompute the token vectors, and validate control frames against the schema.
{
  const relay = JSON.parse(await readFile(join(currentDir, "..", "fixtures", "voice-relay", "v2.json"), "utf8"));
  const schema = JSON.parse(
    await readFile(join(currentDir, "..", "..", "shared-schemas", "schemas", "voice-relay-control.schema.json"), "utf8")
  );
  if (relay.suite !== "ubag.voice-relay.v2" || relay.protocol_version !== 2) {
    errors.push("voice-relay fixture must be suite ubag.voice-relay.v2, protocol_version 2");
  }
  const max = relay.constants?.max_frame_bytes;
  const framingIds = new Set();
  for (const c of relay.framing ?? []) {
    const where = `voice-relay framing ${c.id}`;
    if (framingIds.has(c.id)) errors.push(`${where} duplicated`);
    framingIds.add(c.id);
    if (c.outcome !== "frame" && c.outcome !== "violation") errors.push(`${where}: bad outcome`);
    const payloadLen = c.payload_hex !== undefined ? c.payload_hex.length / 2 : c.payload_len;
    if (c.outcome === "frame") {
      if (!Number.isInteger(payloadLen) || c.length !== payloadLen + 1) errors.push(`${where}: length must be payload+1`);
      if (c.length < 1 || c.length > max) errors.push(`${where}: valid frame length out of 1..max`);
      if (![1, 2].includes(c.type)) errors.push(`${where}: valid frame type must be 1 or 2`);
    } else if (c.length >= 1 && c.length <= max && [1, 2].includes(c.type)) {
      errors.push(`${where}: violation case is actually valid`);
    }
  }
  for (const v of relay.token_vectors ?? []) {
    const want = createHmac("sha256", v.secret).update(`voice-relay|${v.session_id}|${v.exp}`).digest("hex");
    if (want !== v.token) errors.push(`voice-relay token vector ${v.session_id}/${v.exp} does not match HMAC-SHA256`);
  }
  const unboundTokens = new Set((relay.token_vectors ?? []).map((v) => v.token));
  for (const v of relay.bound_token_vectors ?? []) {
    const want = createHmac("sha256", v.secret)
      .update(`voice-relay|${v.session_id}|${v.exp}|${v.node_id}|${v.generation}`)
      .digest("hex");
    if (want !== v.token) errors.push(`voice-relay bound token vector ${v.session_id}/${v.node_id}/${v.generation} does not match HMAC-SHA256`);
    if (unboundTokens.has(v.token)) errors.push(`voice-relay bound token vector ${v.session_id}/${v.node_id} equals an unbound token`);
    if (!Number.isSafeInteger(v.generation) || v.generation < 1) errors.push(`voice-relay bound token vector ${v.node_id}: generation must be a safe integer >= 1`);
  }
  const reasons = [...(relay.reply_reasons?.handshake ?? []), ...(relay.reply_reasons?.session ?? [])];
  const schemaReasons = schema.oneOf.find((s) => s.properties?.op?.const === "error").properties.reason.enum;
  if (JSON.stringify([...reasons].sort()) !== JSON.stringify([...schemaReasons].sort())) {
    errors.push("voice-relay reply_reasons must equal the control schema error.reason enum");
  }
  const helloIds = new Set();
  for (const c of relay.hello_validation ?? []) {
    if (helloIds.has(c.id)) errors.push(`voice-relay hello case ${c.id} duplicated`);
    helloIds.add(c.id);
    if (c.outcome !== "ok" && !reasons.includes(c.outcome)) errors.push(`voice-relay hello case ${c.id}: unknown outcome`);
  }
  for (const c of relay.control_frames ?? []) {
    const matches = schema.oneOf.filter((branch) => schemaErrors(branch, c.json).length === 0).length;
    if ((matches === 1) !== c.valid) errors.push(`voice-relay control frame ${c.id}: schema verdict != valid:${c.valid}`);
  }
}

// Minimal JSON Schema subset (type/const/enum/required/properties/
// additionalProperties/min-maxLength/pattern) so this script needs no ajv.
function schemaErrors(schema, value) {
  const out = [];
  if (schema.const !== undefined && value !== schema.const) out.push("const");
  if (schema.enum && !schema.enum.includes(value)) out.push("enum");
  if (schema.type) {
    const ok =
      schema.type === "object" ? value !== null && typeof value === "object" && !Array.isArray(value)
      : schema.type === "integer" ? Number.isInteger(value)
      : typeof value === schema.type;
    if (!ok) return [...out, "type"];
  }
  if (typeof value === "string") {
    if (schema.minLength !== undefined && value.length < schema.minLength) out.push("minLength");
    if (schema.maxLength !== undefined && value.length > schema.maxLength) out.push("maxLength");
    if (schema.pattern && !new RegExp(schema.pattern).test(value)) out.push("pattern");
  }
  if (typeof value === "number") {
    if (schema.minimum !== undefined && value < schema.minimum) out.push("minimum");
    if (schema.maximum !== undefined && value > schema.maximum) out.push("maximum");
  }
  if (schema.type === "object") {
    for (const key of schema.required ?? []) if (!(key in value)) out.push(`required:${key}`);
    for (const [key, sub] of Object.entries(schema.properties ?? {})) {
      if (key in value) out.push(...schemaErrors(sub, value[key]).map((e) => `${key}.${e}`));
    }
    if (schema.additionalProperties === false) {
      for (const key of Object.keys(value)) if (!(key in (schema.properties ?? {}))) out.push(`extra:${key}`);
    }
  }
  return out;
}

await validateWorkerDaemonFixture();
await validateSseResumeFixture();
await validateNodeAllocationFixture();
await validateBodiesAgainstOpenApi();

if (errors.length > 0) {
  console.error(errors.join("\n"));
  process.exit(1);
}

console.log(`Validated ${fixture.scenarios.length} conformance scenarios from ${fixturePath}`);

function requireString(value, field) {
  if (typeof value !== "string" || value.length === 0) {
    errors.push(`${field} must be a non-empty string`);
  }
}

// Node allocation consumer schema (P2.4): every fixture verdict must match the schema.
// SSE/gRPC resume and close semantics (P2.7): the events are valid JobEvents
// with strictly increasing sequence, and a reference resolver of the documented
// rules (cursor = max(after_sequence, sequence of Last-Event-ID); 204 at or past
// the terminal event; unknown Last-Event-ID ignored; negative cursor 400)
// reproduces every case.
async function validateSseResumeFixture() {
  const schemaDir = join(currentDir, "..", "..", "shared-schemas", "schemas");
  const eventSchema = JSON.parse(await readFile(join(schemaDir, "job-event.schema.json"), "utf8"));
  const ajv = new Ajv2020({ strict: false, allErrors: true });
  addFormats(ajv);
  const validateEvent = ajv.compile(eventSchema);
  const fx = JSON.parse(await readFile(join(currentDir, "..", "fixtures", "streaming", "sse-resume.json"), "utf8"));
  if (fx.suite !== "ubag.streaming.sse-resume.v1") errors.push("sse-resume fixture must be suite ubag.streaming.sse-resume.v1");
  const terminal = new Set(["completed", "completed_with_warnings", "failed_retryable", "failed_terminal", "dead_letter", "cancelled", "timed_out"]);
  const events = fx.events ?? [];
  let prev = 0;
  const ids = new Set();
  for (const e of events) {
    if (!validateEvent(e)) errors.push(`sse-resume event ${e.event_id} violates job-event schema`);
    if (e.sequence <= prev) errors.push(`sse-resume event ${e.event_id}: sequence must be strictly increasing`);
    if (ids.has(e.event_id)) errors.push(`sse-resume duplicate event_id ${e.event_id}`);
    prev = e.sequence;
    ids.add(e.event_id);
  }
  const terminalSeq = events.find((e) => terminal.has(e.type))?.sequence ?? Infinity;
  const resolve = ({ after_sequence, last_event_id }) => {
    if (after_sequence !== undefined && (!Number.isInteger(after_sequence) || after_sequence < 0)) return { status: 400, frames: [] };
    const headerSeq = events.find((e) => e.event_id === last_event_id)?.sequence ?? 0;
    const cursor = Math.max(after_sequence ?? 0, headerSeq);
    if (cursor >= terminalSeq) return { status: 204, frames: [] };
    return { status: 200, frames: events.filter((e) => e.sequence > cursor).map((e) => e.event_id) };
  };
  const seen = new Set();
  for (const c of fx.cases ?? []) {
    if (seen.has(c.id)) errors.push(`sse-resume case ${c.id} duplicated`);
    seen.add(c.id);
    const got = resolve(c.request ?? {});
    if (got.status !== c.expect.status || JSON.stringify(got.frames) !== JSON.stringify(c.expect.frames)) {
      errors.push(`sse-resume case ${c.id}: reference resolver gave ${JSON.stringify(got)}, fixture expects ${JSON.stringify(c.expect)}`);
    }
    if (c.expect.ends_with_terminal && events.find((e) => e.event_id === c.expect.frames.at(-1))?.sequence !== terminalSeq) {
      errors.push(`sse-resume case ${c.id}: ends_with_terminal but last frame is not the terminal event`);
    }
  }
  if (!seen.size) errors.push("sse-resume fixture has no cases");
}

async function validateNodeAllocationFixture() {
  const schemaDir = join(currentDir, "..", "..", "shared-schemas", "schemas");
  const schema = JSON.parse(await readFile(join(schemaDir, "node-allocation.schema.json"), "utf8"));
  const ajv = new Ajv2020({ strict: false, allErrors: true });
  addFormats(ajv);
  const validateOne = ajv.compile(schema);
  const validateList = ajv.compile({ $ref: `${schema.$id}#/$defs/allocation_list` });
  const fx = JSON.parse(await readFile(join(currentDir, "..", "fixtures", "node-allocation", "v1.json"), "utf8"));
  if (fx.suite !== "ubag.node-allocation.v1" || fx.schema_version !== 1) {
    errors.push("node-allocation fixture must be suite ubag.node-allocation.v1, schema_version 1");
  }
  for (const [cases, validate, label] of [[fx.allocations, validateOne, "allocation"], [fx.lists, validateList, "list"]]) {
    const seen = new Set();
    for (const c of cases ?? []) {
      if (seen.has(c.id)) errors.push(`node-allocation ${label} ${c.id} duplicated`);
      seen.add(c.id);
      if (validate(c.json) !== c.valid) errors.push(`node-allocation ${label} ${c.id}: schema verdict != valid:${c.valid}`);
    }
    if (!seen.size) errors.push(`node-allocation fixture has no ${label} cases`);
  }
}

// Worker daemon protocol v2 vectors (P2.2): every line validates against its
// schema, event ids follow the attempt-scoped derivation, tokens are provisional
// (result only from a terminal), and an abandoned attempt commits nothing.
async function validateWorkerDaemonFixture() {
  const { createHash } = await import("node:crypto");
  const schemaDir = join(currentDir, "..", "..", "shared-schemas", "schemas");
  const load = async (name) => JSON.parse(await readFile(join(schemaDir, name), "utf8"));
  const [requestSchema, endSchema, eventSchema] = await Promise.all([
    load("worker-daemon-request.schema.json"),
    load("worker-daemon-job-end.schema.json"),
    load("job-event.schema.json")
  ]);
  const ajv = new Ajv2020({ strict: false, allErrors: true });
  addFormats(ajv);
  const validateRequest = ajv.compile(requestSchema);
  const validateEnd = ajv.compile(endSchema);
  const validateReply = ajv.compile({ $ref: `${endSchema.$id}#/$defs/control_reply` });
  const validateEvent = ajv.compile(eventSchema);

  const fx = JSON.parse(await readFile(join(currentDir, "..", "fixtures", "worker-daemon", "v2.json"), "utf8"));
  if (fx.suite !== "ubag.worker-daemon.v2" || fx.protocol_version !== 2) {
    errors.push("worker-daemon fixture must be suite ubag.worker-daemon.v2, protocol_version 2");
  }
  const expectVerdicts = (cases, validate, label) => {
    const seen = new Set();
    for (const c of cases ?? []) {
      if (seen.has(c.id)) errors.push(`worker-daemon ${label} ${c.id} duplicated`);
      seen.add(c.id);
      if (validate(c.json) !== c.valid) errors.push(`worker-daemon ${label} ${c.id}: schema verdict != valid:${c.valid}`);
    }
    if (!seen.size) errors.push(`worker-daemon fixture has no ${label} cases`);
  };
  expectVerdicts(fx.requests, validateRequest, "request");
  expectVerdicts(fx.job_ends, validateEnd, "job_end");
  expectVerdicts(fx.control_replies, validateReply, "control_reply");

  const eventId = (e) =>
    "evt_" + createHash("sha256").update(`${e.job_id}:${e.data.attempt_id ?? ""}${e.data.attempt_id ? ":" : ""}${e.sequence}`).digest("hex").slice(0, 16);

  // Returns the validated events and the JOB_END marker (if any) of a stdout transcript.
  const parseRun = (lines, label) => {
    const events = [];
    let marker = null;
    for (const line of lines) {
      if (line.__ubag_job_end__ === true) {
        if (marker) errors.push(`${label}: more than one JOB_END`);
        marker = line;
        if (!validateEnd(line)) errors.push(`${label}: JOB_END invalid: ${ajv.errorsText(validateEnd.errors)}`);
        continue;
      }
      if (marker) errors.push(`${label}: event after JOB_END`);
      if (!validateEvent(line)) errors.push(`${label}: event seq ${line.sequence} invalid: ${ajv.errorsText(validateEvent.errors)}`);
      if (line.event_id !== eventId(line)) errors.push(`${label}: event seq ${line.sequence} event_id is not the attempt-scoped derivation`);
      if (events.length && line.sequence !== events[events.length - 1].sequence + 1) errors.push(`${label}: sequence not monotonic at ${line.sequence}`);
      events.push(line);
    }
    return { events, marker };
  };
  const isToken = (e) => e.type === "token" || e.type === "token_streaming";
  const terminals = new Set(["completed", "completed_with_warnings", "failed_retryable", "failed_terminal", "timed_out", "cancelled", "dead_letter"]);
  const checkCommitted = (run, label, attemptId, resultSeq) => {
    const { events, marker } = run;
    if (!marker || marker.status !== "completed") errors.push(`${label}: committed run needs JOB_END status completed`);
    if (marker && marker.attempt_id !== attemptId) errors.push(`${label}: JOB_END attempt_id mismatch`);
    const done = events.filter((e) => terminals.has(e.type));
    if (done.length !== 1) errors.push(`${label}: exactly one terminal event required`);
    for (const e of events) {
      if (e.data.attempt_id !== attemptId) errors.push(`${label}: event seq ${e.sequence} missing attempt_id`);
      if (isToken(e) && "result" in e.data) errors.push(`${label}: token event seq ${e.sequence} must not carry a result`);
    }
    if (done[0] && (done[0].sequence !== resultSeq || !done[0].data.result)) errors.push(`${label}: result must come from terminal seq ${resultSeq}`);
  };

  const s = fx.streamed_job;
  if (!validateRequest(s.request_line) || s.request_line.proto !== 2) errors.push("worker-daemon streamed_job request_line must be a valid proto:2 job line");
  const streamed = parseRun(s.stdout_lines, "streamed_job");
  checkCommitted(streamed, "streamed_job", s.request_line.attempt.id, s.expect.result_from_sequence);
  const tokenSeqs = streamed.events.filter(isToken).map((e) => e.sequence);
  if (JSON.stringify(tokenSeqs) !== JSON.stringify(s.expect.provisional_sequences)) errors.push("worker-daemon streamed_job provisional_sequences do not match the token events");

  const idsByAttempt = [];
  for (const a of fx.abandoned_attempt.attempts) {
    const label = `abandoned_attempt/${a.attempt_id}`;
    const run = parseRun(a.stdout_lines, label);
    idsByAttempt.push(new Set(run.events.map((e) => e.event_id)));
    if (a.expect.outcome === "abandoned") {
      if (run.marker || run.events.some((e) => terminals.has(e.type))) errors.push(`${label}: an abandoned attempt has no terminal event and no JOB_END`);
      if (!run.events.some(isToken)) errors.push(`${label}: abandoned fixture must have streamed tokens`);
    } else {
      checkCommitted(run, label, a.attempt_id, a.expect.result_from_sequence);
    }
  }
  if (idsByAttempt.length === 2 && [...idsByAttempt[0]].some((id) => idsByAttempt[1].has(id))) {
    errors.push("abandoned_attempt: attempts must not share event ids even with equal sequence numbers");
  }

  for (const f of fx.failure_semantics ?? []) {
    const label = `failure_semantics/${f.id}`;
    const { events, marker } = parseRun(f.stdout_lines, label);
    const last = events[events.length - 1];
    if (!marker || marker.status === "completed") errors.push(`${label}: JOB_END must not be completed`);
    if (!last || !terminals.has(last.type) || last.type === "completed") errors.push(`${label}: must end with a non-completed terminal event`);
    if (last?.type === "timed_out" && (!last.data.partial || "result" in last.data)) errors.push(`${label}: timed_out needs data.partial and no result`);
    if (last?.data.reconcile_required && (last.type !== "failed_terminal" || last.data.submitted !== true)) errors.push(`${label}: reconcile_required needs failed_terminal + submitted:true`);
  }
  // Schema-level guard: reconcile_required without submitted:true is rejected.
  const bad = { ...fx.failure_semantics[1].stdout_lines[2], data: { reconcile_required: true } };
  if (validateEvent(bad)) errors.push("job-event schema must reject reconcile_required without submitted:true");
}

// Ajv body validation: browser, concurrency and jobs scenarios must match the
// OpenAPI request/response schemas (field names and shapes), not just structure.
async function validateBodiesAgainstOpenApi() {
  const specDir = join(currentDir, "..", "..", "openapi");
  const spec = parseYaml(await readFile(join(specDir, "openapi.yaml"), "utf8"));
  const ajv = new Ajv2020({ strict: false, allErrors: true });
  addFormats(ajv);
  // External shared-schema $refs (JobRequest/JobResponse/...) are registered by
  // their own $id and the component becomes a ref to it.
  for (const [name, schema] of Object.entries(spec.components.schemas)) {
    if (typeof schema.$ref === "string" && !schema.$ref.startsWith("#")) {
      const external = JSON.parse(await readFile(join(specDir, schema.$ref), "utf8"));
      ajv.addSchema(external);
      spec.components.schemas[name] = { $ref: external.$id };
    }
  }
  ajv.addSchema({ $id: "urn:ubag:openapi", ...spec });

  const check = (ref, value, label) => {
    const validate = ajv.compile({ $ref: `urn:ubag:openapi#${ref}` });
    if (!validate(value)) {
      errors.push(`${label} violates OpenAPI ${ref}: ${ajv.errorsText(validate.errors)}`);
    }
  };
  const jsonSchemaRef = (content) => content?.["application/json"]?.schema?.$ref?.replace(/^#/, "") ?? null;

  const templates = Object.keys(spec.paths).map((template) => ({
    template,
    re: new RegExp(`^${template.replace(/\{[^}]+\}/g, "[^/]+")}$`)
  }));

  for (const scenario of fixture.scenarios ?? []) {
    const covered = ["browser", "concurrency", "jobs"].includes(scenario.category) || scenario.id.startsWith("jobs.");
    if (!covered || !scenario.request || !scenario.response) continue;
    const label = `scenario ${scenario.id}`;
    const path = String(scenario.request.path).split("?")[0];
    const match = templates.find((t) => t.re.test(path));
    const operation = match && spec.paths[match.template][String(scenario.request.method).toLowerCase()];
    if (!operation) {
      errors.push(`${label}: ${scenario.request.method} ${path} is not an OpenAPI operation`);
      continue;
    }
    if (scenario.request.body) {
      const ref = jsonSchemaRef(operation.requestBody?.content);
      if (ref) check(ref, scenario.request.body, `${label} request body`);
    }
    const body = scenario.response.body;
    if (body && typeof body === "object") {
      const response = operation.responses?.[String(scenario.response.status)];
      const resolved = response?.$ref ? resolvePointer(spec, response.$ref) : response;
      const ref = jsonSchemaRef(resolved?.content);
      if (ref) check(ref, body, `${label} response body`);
      else errors.push(`${label}: OpenAPI declares no JSON body schema for status ${scenario.response.status}`);
    }
  }
}

function resolvePointer(root, ref) {
  return ref.replace(/^#\//, "").split("/").reduce((node, key) => node?.[key], root);
}
