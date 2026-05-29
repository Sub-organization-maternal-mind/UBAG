import {
  OBSERVABILITY_EVENT_NAMES,
  OBSERVABILITY_METRICS,
  SMOKE_CHECKLIST,
  validateEventRegistry,
  validateHealthProbeRegistry,
  validateMetricRegistry,
  validateSmokeChecklist
} from "../src/index.mjs";

const failures = [
  ...validateMetricRegistry(),
  ...validateEventRegistry(),
  ...validateHealthProbeRegistry(),
  ...validateSmokeChecklist()
];

if (failures.length > 0) {
  console.error(`Observability contract validation failed:\n${failures.map((failure) => `- ${failure}`).join("\n")}`);
  process.exit(1);
}

console.log(
  [
    "Observability contracts passed:",
    `${OBSERVABILITY_METRICS.length} metrics`,
    `${OBSERVABILITY_EVENT_NAMES.length} event names`,
    `${SMOKE_CHECKLIST.length} smoke checks`
  ].join(" ")
);
