import { existsSync } from "node:fs";
import { join } from "node:path";
import { spawnSync } from "node:child_process";

const root = process.cwd();
const script = join(root, "deploy", "small", "small.ps1");

if (!existsSync(script)) {
  console.error(`Small profile script not found at ${script}`);
  process.exit(1);
}

const executable = findPowerShell();
if (!executable) {
  console.error("PowerShell is required to run deploy/small/small.ps1.");
  process.exit(1);
}

const args = process.platform === "win32"
  ? ["-NoProfile", "-ExecutionPolicy", "Bypass", "-File", script, "-Action", "smoke"]
  : ["-NoProfile", "-File", script, "-Action", "smoke"];
const result = spawnSync(executable, args, { cwd: root, stdio: "inherit" });

if (result.error) {
  console.error(result.error.message);
  process.exit(1);
}
process.exit(result.status ?? 1);

function findPowerShell() {
  const candidates = process.platform === "win32" ? ["pwsh.exe", "powershell.exe"] : ["pwsh", "powershell"];
  for (const candidate of candidates) {
    const probe = spawnSync(candidate, ["-NoProfile", "-Command", "$PSVersionTable.PSVersion.ToString()"], {
      cwd: root,
      stdio: "ignore"
    });
    if (!probe.error && probe.status === 0) {
      return candidate;
    }
  }
  return undefined;
}
