#!/usr/bin/env node
/**
 * Validates the nginx-dashboard config.
 * Checks that deploy/small/nginx-dashboard/nginx.conf exists and contains
 * the required routing directives for the UBAG small profile.
 */
import { readFileSync } from 'node:fs';
import { resolve, dirname } from 'node:path';
import { fileURLToPath } from 'node:url';

const root = resolve(dirname(fileURLToPath(import.meta.url)), '..');
const configPath = resolve(root, 'deploy/small/nginx-dashboard/default.conf.template');

let config;
try {
  config = readFileSync(configPath, 'utf8');
} catch {
  console.error(`[check-nginx-dashboard] ERROR: config not found at ${configPath}`);
  process.exit(1);
}

const REQUIRED = [
  { label: 'gateway upstream',       pattern: /upstream\s+ubag_gateway/         },
  { label: 'docker DNS resolver',     pattern: /resolver\s+127\.0\.0\.11/       },
  { label: '/v1/ proxy',             pattern: /location\s+\/v1\//               },
  { label: '/novnc/ proxy',          pattern: /location\s+\/novnc\//            },
  { label: 'dynamic noVNC proxy',     pattern: /set\s+\$ubag_browser_viewer\s+http:\/\/browser-viewer:6080/ },
  { label: '/_app/immutable/ cache', pattern: /location\s+\/dashboard\/_app\/immutable\//  },
  { label: '/_app/ static',          pattern: /location\s+\/dashboard\/_app\//             },
  { label: '/dashboard/ SPA',        pattern: /location\s+\/dashboard\//        },
  { label: 'try_files SPA fallback', pattern: /try_files.*index\.html/          },
  { label: 'sw.js no-cache',         pattern: /sw\.js/                          },
  { label: 'healthz',                pattern: /\/healthz/                        },
  { label: 'metrics/ready blocked',   pattern: /metrics|ready/                   },
  { label: 'Basic Auth gate',        pattern: /auth_basic\s+"UBAG/              },
  { label: 'server-side Bearer inject', pattern: /Bearer \$\{UBAG_GATEWAY_SECRET\}/ },
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
