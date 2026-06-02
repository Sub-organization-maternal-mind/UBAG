#!/usr/bin/env node
/**
 * Validates the nginx-dashboard config (replaces check-caddy.mjs).
 * Checks that deploy/small/nginx-dashboard/nginx.conf exists and contains
 * the required routing directives for the UBAG small profile.
 */
import { readFileSync } from 'node:fs';
import { resolve, dirname } from 'node:path';
import { fileURLToPath } from 'node:url';

const root = resolve(dirname(fileURLToPath(import.meta.url)), '..');
const configPath = resolve(root, 'deploy/small/nginx-dashboard/nginx.conf');

let config;
try {
  config = readFileSync(configPath, 'utf8');
} catch {
  console.error(`[check-nginx-dashboard] ERROR: config not found at ${configPath}`);
  process.exit(1);
}

const REQUIRED = [
  { label: 'gateway upstream',       pattern: /upstream\s+ubag_gateway/         },
  { label: '/v1/ proxy',             pattern: /location\s+\/v1\//               },
  { label: '/novnc/ proxy',          pattern: /location\s+\/novnc\//            },
  { label: '/_app/immutable/ cache', pattern: /location\s+\/_app\/immutable\//  },
  { label: '/_app/ static',          pattern: /location\s+\/_app\//             },
  { label: '/dashboard/ SPA',        pattern: /location\s+\/dashboard\//        },
  { label: 'try_files SPA fallback', pattern: /try_files.*index\.html/          },
  { label: 'sw.js no-cache',         pattern: /sw\.js/                          },
  { label: 'healthz',                pattern: /\/healthz/                        },
  { label: 'metrics/ready blocked',   pattern: /metrics|ready/                   },
];

let passed = true;
for (const { label, pattern } of REQUIRED) {
  if (!pattern.test(config)) {
    console.error(`[check-nginx-dashboard] MISSING: ${label}`);
    passed = false;
  }
}

if (passed) {
  console.log(`[check-nginx-dashboard] OK — all required directives present in ${configPath}`);
} else {
  process.exit(1);
}
