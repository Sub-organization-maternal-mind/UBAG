#!/usr/bin/env node
import { readFileSync, writeFileSync, readdirSync, statSync } from 'node:fs';
import { createHash } from 'node:crypto';
import { fileURLToPath } from 'node:url';
import { dirname, resolve, join } from 'node:path';

const dir = dirname(fileURLToPath(import.meta.url));
const distDir = resolve(dir, '../dist');

// adapter-static prerenders one HTML file per route (dist/index.html,
// dist/settings/index.html, dist/jobs/index.html, ...), and each carries its
// own inline SvelteKit hydration bootstrap script with route-specific content
// (and therefore its own sha256 hash). Patching only dist/index.html leaves
// every other route's CSP without its script's hash, so its own hydration
// script is blocked and the page loads as dead, non-interactive HTML. Walk
// every prerendered HTML file and patch each one independently.
function findHtmlFiles(root) {
  const out = [];
  for (const entry of readdirSync(root)) {
    const full = join(root, entry);
    const stat = statSync(full);
    if (stat.isDirectory()) {
      out.push(...findHtmlFiles(full));
    } else if (entry.endsWith('.html')) {
      out.push(full);
    }
  }
  return out;
}

function patchFile(file) {
  let html = readFileSync(file, 'utf8');

  // Extract all inline scripts (no src attribute) in this file.
  const inlineScriptRe = /<script(?![^>]*\bsrc\s*=)[^>]*>([\s\S]*?)<\/script>/gi;
  const hashes = [];
  let m;
  while ((m = inlineScriptRe.exec(html)) !== null) {
    const content = m[1];
    if (content.trim()) {
      const hash = createHash('sha256').update(content).digest('base64');
      hashes.push(`'sha256-${hash}'`);
    }
  }

  if (hashes.length === 0) {
    return { file, hashes: [] };
  }

  // Patch script-src in the CSP meta tag by parsing CSP directives correctly.
  // CSP directives are semicolon-separated — we must NOT match past the ';'.
  html = html.replace(
    /(<meta[^>]+Content-Security-Policy[^>]+content=")([^"]+)(")/i,
    (_, pre, csp, post) => {
      const directives = csp.split(';').map(d => d.trim());
      const patched = directives.map(d => {
        if (d.startsWith('script-src')) {
          const extra = hashes.filter(h => !d.includes(h)).join(' ');
          return extra ? `${d} ${extra}` : d;
        }
        return d;
      });
      return `${pre}${patched.join('; ')}${post}`;
    }
  );

  writeFileSync(file, html);
  return { file, hashes };
}

const htmlFiles = findHtmlFiles(distDir);
if (htmlFiles.length === 0) {
  console.log('No HTML files found under dist/ — CSP unchanged.');
  process.exit(0);
}

let totalHashes = 0;
for (const file of htmlFiles) {
  const { hashes } = patchFile(file);
  totalHashes += hashes.length;
  const rel = file.slice(distDir.length + 1);
  if (hashes.length > 0) {
    console.log(`  ${rel}: patched with ${hashes.length} inline script hash(es)`);
  }
}
console.log(`CSP patched across ${htmlFiles.length} HTML file(s), ${totalHashes} inline script hash(es) total.`);
