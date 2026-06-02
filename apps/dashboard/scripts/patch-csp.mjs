#!/usr/bin/env node
import { readFileSync, writeFileSync } from 'node:fs';
import { createHash } from 'node:crypto';
import { fileURLToPath } from 'node:url';
import { dirname, resolve } from 'node:path';

const dir = dirname(fileURLToPath(import.meta.url));
const distHtml = resolve(dir, '../dist/index.html');

let html = readFileSync(distHtml, 'utf8');

// Extract all inline scripts (no src attribute)
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
  console.log('No inline scripts found — CSP unchanged.');
  process.exit(0);
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

writeFileSync(distHtml, html);
console.log(`CSP patched with ${hashes.length} inline script hash(es): ${hashes.join(', ')}`);
