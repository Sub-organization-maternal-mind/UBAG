import { readFile } from 'node:fs/promises';
import { dirname, resolve } from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';

const scriptDir = dirname(fileURLToPath(import.meta.url));
const appRoot = resolve(scriptDir, '..');
const srcDir = resolve(appRoot, 'src');
const expectedTabs = [
  'overview',
  'apps',
  'targets',
  'jobs',
  'sessions',
  'templates',
  'runtime',
  'activation'
];

const [html, appCss, tokensCss] = await Promise.all([
  readFile(resolve(srcDir, 'index.html'), 'utf8'),
  readFile(resolve(srcDir, 'styles', 'app.css'), 'utf8'),
  readFile(resolve(srcDir, 'styles', 'tokens.css'), 'utf8')
]);

const { dashboardData, dashboardTabs } = await import(
  pathToFileURL(resolve(srcDir, 'mock-data.js')).href
);

const failures = [];
const assert = (condition, message) => {
  if (!condition) failures.push(message);
};

assert(html.includes('role="tablist"'), 'tablist role is missing');
assert(html.includes('aria-label="Dashboard views"'), 'tablist label is missing');

for (const tab of expectedTabs) {
  assert(dashboardTabs.includes(tab), `mock-data is missing ${tab} tab`);
  assert(html.includes(`id="tab-${tab}"`), `${tab} tab button is missing`);
  assert(
    html.includes(`aria-controls="panel-${tab}"`),
    `${tab} aria-controls is missing`
  );
  assert(
    appCss.includes(`#panel-${tab}`) || appCss.includes('.view-panel'),
    `${tab} panel styling is not covered`
  );
}

for (const collection of [
  'summary',
  'operatorItems',
  'apps',
  'targets',
  'jobs',
  'sessions',
  'templates',
  'runtime',
  'activation',
  'activity'
]) {
  assert(Array.isArray(dashboardData[collection]), `${collection} must be an array`);
  assert(dashboardData[collection].length > 0, `${collection} needs local mock rows`);
}

assert(Array.isArray(dashboardData.stateFixtures), 'stateFixtures must be an array');
for (const state of ['loading', 'empty', 'partial', 'error', 'permission-denied', 'stale']) {
  assert(
    dashboardData.stateFixtures.some((item) => item.state === state),
    `${state} dashboard state fixture is missing`
  );
  assert(appCss.includes(`.state-${state}`), `${state} dashboard state styling is missing`);
}

for (const token of [
  '--color-paper',
  '--color-accent',
  '--color-marine',
  '--font-display',
  '--space-4',
  '--ease-out',
  '--radius-md'
]) {
  assert(tokensCss.includes(token), `${token} token is missing`);
}

assert(
  /html,\s*body\s*{[^}]*overflow-x:\s*clip/s.test(appCss),
  'html/body must use overflow-x: clip'
);

for (const width of ['768px', '414px', '375px', '320px']) {
  assert(appCss.includes(`max-width: ${width}`), `${width} responsive gate is missing`);
}

assert(appCss.includes(':focus-visible'), 'focus-visible styles are missing');
assert(appCss.includes(':disabled'), 'disabled styles are missing');
assert(appCss.includes('[aria-busy="true"]'), 'loading state styles are missing');
assert(appCss.includes('.is-error'), 'error state styles are missing');
assert(appCss.includes('.is-success'), 'success state styles are missing');
assert(html.includes('Content-Security-Policy'), 'dashboard CSP is missing');
assert(!html.includes('fonts.googleapis.com'), 'dashboard must not load Google Fonts');
assert(!html.includes('fonts.gstatic.com'), 'dashboard must not load Google Fonts assets');
assert(html.includes('role="status"'), 'refresh status live region is missing');
for (const tab of ['runtime', 'activation']) {
  assert(dashboardTabs.includes(tab), `${tab} dashboard tab is missing`);
}

const forbiddenClaims = /trusted by|customer logos|conversion|revenue|\d+\s*x\s*faster/i;
assert(!forbiddenClaims.test(JSON.stringify(dashboardData)), 'mock data has claim-like copy');

if (failures.length > 0) {
  console.error('Dashboard check failed:');
  for (const failure of failures) console.error(`- ${failure}`);
  process.exit(1);
}

console.log('Dashboard check passed');
