import { renderSmokeChecklistMarkdown, validateSmokeChecklist } from "../src/index.mjs";

const failures = validateSmokeChecklist();
if (failures.length > 0) {
  console.error(`Smoke checklist validation failed:\n${failures.map((failure) => `- ${failure}`).join("\n")}`);
  process.exit(1);
}

process.stdout.write(renderSmokeChecklistMarkdown());
