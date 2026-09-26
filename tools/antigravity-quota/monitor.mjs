#!/usr/bin/env node

import { execFile } from "node:child_process";
import { promisify } from "node:util";

const execFileAsync = promisify(execFile);

const GATEWAY_URL = process.env.UBAG_GATEWAY_URL || "http://localhost:8080";
const APP_SECRET = process.env.UBAG_APP_SECRET || "dev-secret";
const AGY_QUOTA_BIN = process.env.AGY_QUOTA_BIN || "agy-quota";

async function main() {
  try {
    const { stdout } = await execFileAsync(AGY_QUOTA_BIN, ["-c", "-d"], {
      timeout: 30000,
      maxBuffer: 1024 * 1024,
    });

    const quotaData = JSON.parse(stdout);

    const response = await fetch(`${GATEWAY_URL}/v1/quotas/antigravity/refresh`, {
      method: "POST",
      headers: {
        "Content-Type": "application/json",
        "Authorization": `Bearer ${APP_SECRET}`,
      },
      body: JSON.stringify(quotaData),
    });

    if (!response.ok) {
      console.error(`Gateway returned ${response.status}: ${await response.text()}`);
      process.exit(1);
    }

    console.log("Quota data refreshed successfully");
  } catch (error) {
    console.error(`Failed to refresh quota: ${error.message}`);
    process.exit(1);
  }
}

main();
