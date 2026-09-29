// A QA and performance pass over a running gnoscope, kept out of CI on purpose.
//
// This is not the e2e suite. That one runs on every push, against a seeded
// fixture, and answers "did this change break the code". This one is run by
// hand against a real deployment and answers a different question: is every
// page still drawing, and has anything got slower than it used to be.
//
// Two things it does that e2e deliberately does not:
//
//   - It measures. e2e asserts and throws away the numbers, so a page that
//     doubled in cost while staying correct passes it silently.
//   - It records. Every run appends a file under qa/results/, and report.mjs
//     pivots those into a table, so the unit of interest is the trend rather
//     than any single pass.
//
// What each page check asserts is "something specific was drawn", never "no
// error was raised". On 2026-09-29 /dashboards spent 40 minutes on production
// rendering the words "charts unavailable" and nothing else: no exception, no
// failed request, no empty <main>, and a full green e2e suite over the top of
// it. A check that can be satisfied by a page politely reporting its own
// failure is not a check. Hence `proof`, a selector that only exists when the
// page did its job.
//
//   node qa/run.mjs                        the target in checks.json
//   node qa/run.mjs --target http://…      somewhere else, e.g. a local build
//   node qa/run.mjs --only dashboards,gas  a subset, by check id
//   node qa/run.mjs --no-store             print, record nothing
//
// Requires the Playwright browser from e2e/ (cd e2e && npm ci).

import { chromium } from '../e2e/node_modules/playwright/index.mjs';
import { mkdirSync, readFileSync, writeFileSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

const qaDir = dirname(fileURLToPath(import.meta.url));
const checks = JSON.parse(readFileSync(join(qaDir, 'checks.json'), 'utf8'));

const argv = process.argv.slice(2);
const flag = (name, fallback) => {
  const i = argv.indexOf(`--${name}`);
  return i === -1 ? fallback : argv[i + 1];
};
const target = (flag('target', checks.target) || '').replace(/\/$/, '');
const only = (flag('only', '') || '').split(',').filter(Boolean);
const store = !argv.includes('--no-store');

const budgets = checks.budgets || {};
const wanted = (c) => only.length === 0 || only.includes(c.id);

// A page is "ready" when the thing it exists to show is on screen. Without a
// `proof` of its own a page falls back to this, which is the weakest useful
// assertion: the active view drew at least one element into its main.
//
// `:not(.skeleton)` is load-bearing and was missing from the first version of
// this file. skeletonBlock() appends `.skeleton.skeleton-line` divs, so the
// bare selector matched the loading placeholder and every page "finished" in
// about 45ms having fetched nothing. Which is the same mistake as the one this
// whole tool exists to catch: a check satisfied by the page saying it is not
// ready yet. Readiness needs both halves below.
const DEFAULT_PROOF = '.view.active main *:not(.skeleton)';

// The other half: no placeholder left anywhere. Same definition the e2e
// helper `settle()` uses, and for the same reason, except that here the
// interesting output is the clock rather than the assertion.
const SKELETONS_GONE = () => document.querySelectorAll('.skeleton, .shimmer').length === 0;

// Third-party hosts are counted separately because keeping them off the first
// paint is a property worth watching rather than a one-off fix: the four chart
// libraries were 676 KB of render-blocking JS on every page until #411, and
// nothing would have noticed them creeping back.
const isThirdParty = (url, origin) => /^https?:/.test(url) && !url.startsWith(origin);

// A failure the environment owns rather than the site. Each pattern in
// checks.json carries its reason, so the list cannot quietly grow into a way
// of making a real failure go away.
const ignoreRequests = (checks.ignoreRequests || []).map((r) => r.match);
const ignored = (s) => ignoreRequests.some((m) => s.includes(m));

function verdict(value, budget) {
  if (value === null || budget === undefined) return 'pass';
  return value > budget ? 'slow' : 'pass';
}

async function probePage(browser, check, origin) {
  // A fresh context per page, so every number is a cold visit. Reusing one
  // context measures the second visit, which is not the one anybody complains
  // about.
  const context = await browser.newContext();
  const page = await context.newPage();

  const jsErrors = [];
  const consoleErrors = [];
  const failed = [];
  const thirdParty = new Map();

  page.on('pageerror', (e) => jsErrors.push(String(e).slice(0, 200)));
  page.on('console', (m) => { if (m.type() === 'error' && !ignored(m.text())) consoleErrors.push(m.text().slice(0, 200)); });
  page.on('requestfailed', (r) => {
    if (!ignored(r.url())) failed.push(`${r.url().slice(0, 120)} ${(r.failure() || {}).errorText}`);
  });
  page.on('response', (r) => {
    if (r.status() >= 400 && !ignored(r.url())) failed.push(`${r.url().slice(0, 120)} HTTP ${r.status()}`);
    if (isThirdParty(r.url(), origin)) thirdParty.set(new URL(r.url()).host, true);
  });

  const url = origin + check.path + (check.path.includes('?') ? '&' : '?')
    + 'network=' + encodeURIComponent(checks.network);
  const budget = check.budgetMs ?? budgets.readyMs;
  const timeout = check.timeoutMs ?? Math.max(20000, budget * 3);

  const started = Date.now();
  // Two clocks, because they answer two different complaints and conflating
  // them mislabels both.
  //
  //   readyMs    the reader can see what they came for
  //   settledMs  nothing on the page is still a pulsing grey box
  //
  // They diverge when a page paints its own content quickly and then leaves a
  // background tab loading. /realm/r/gov/dao does exactly that today: the
  // header and tab strip are up in ~150ms and the events tab's skeleton never
  // clears, because /api/events times out. Measured as one number that reads
  // as a broken page, which it is not; measured as two it reads as a page
  // with one slow corner, which it is.
  let readyMs = null;
  let settledMs = null;
  let status = 'pass';
  let note = '';

  try {
    const resp = await page.goto(url, { waitUntil: 'commit', timeout });
    if (resp && resp.status() >= 400) {
      status = 'fail';
      note = `HTTP ${resp.status()}`;
    }
    await page.locator(check.proof || DEFAULT_PROOF).first().waitFor({ state: 'visible', timeout });
    readyMs = Date.now() - started;
  } catch (err) {
    status = 'fail';
    // The selector timing out is the interesting failure and deserves to say
    // which selector, because "the page is broken" and "the proof is stale"
    // look identical from here and are fixed in different files.
    note = note || `no ${check.proof || 'content'} within ${timeout}ms`;
  }

  // Never fatal on its own. A skeleton that outlives the page is a real
  // defect and gets said out loud, but the page did draw.
  try {
    await page.waitForFunction(SKELETONS_GONE, null, { timeout });
    settledMs = Date.now() - started;
  } catch { /* recorded as null: still loading when we gave up */ }

  const nav = await page.evaluate(() => {
    const n = performance.getEntriesByType('navigation')[0];
    const r = performance.getEntriesByType('resource');
    return {
      domInteractiveMs: n ? Math.round(n.domInteractive) : null,
      shellBytes: n ? n.encodedBodySize : null,
      apiRequests: r.filter((x) => x.name.includes('/api/')).length,
      thirdPartyBytes: r.filter((x) => !x.name.startsWith(location.origin))
        .reduce((s, x) => s + (x.transferSize || 0), 0),
    };
  }).catch(() => ({ domInteractiveMs: null, shellBytes: null, apiRequests: null, thirdPartyBytes: null }));

  if (status === 'pass' && jsErrors.length) { status = 'fail'; note = jsErrors[0]; }
  if (status === 'pass' && failed.length) { status = 'fail'; note = failed[0]; }
  if (status === 'pass') status = verdict(readyMs, budget);
  if (status === 'pass') status = verdict(nav.domInteractiveMs, budgets.domInteractiveMs);
  if (status === 'pass' && settledMs === null) {
    status = 'slow';
    note = note || 'drew, but a skeleton was still on screen when we gave up';
  }

  await context.close();

  return {
    id: check.id,
    kind: 'page',
    path: check.path,
    status,
    note,
    budgetMs: budget,
    readyMs,
    settledMs,
    domInteractiveMs: nav.domInteractiveMs,
    apiRequests: nav.apiRequests,
    thirdPartyBytes: nav.thirdPartyBytes,
    thirdPartyHosts: [...thirdParty.keys()].sort(),
    jsErrors: jsErrors.length,
    consoleErrors: consoleErrors.length,
    failedRequests: failed.length,
  };
}

async function probeApi(check, origin) {
  const url = origin + check.path + (check.path.includes('?') ? '&' : '?')
    + 'network=' + encodeURIComponent(checks.network);
  const budget = check.budgetMs ?? budgets.apiMs;
  const timeout = check.timeoutMs ?? Math.max(20000, budget * 3);

  const started = Date.now();
  let httpStatus = null;
  let bytes = null;
  let status = 'pass';
  let note = '';

  try {
    const resp = await fetch(url, { signal: AbortSignal.timeout(timeout) });
    httpStatus = resp.status;
    bytes = (await resp.arrayBuffer()).byteLength;
    if (!resp.ok) { status = 'fail'; note = `HTTP ${resp.status}`; }
  } catch (err) {
    status = 'fail';
    note = String(err).slice(0, 120);
  }
  const ms = Date.now() - started;
  if (status === 'pass') status = verdict(ms, budget);

  return { id: check.id, kind: 'api', path: check.path, status, note, budgetMs: budget, ms, httpStatus, bytes };
}

async function main() {
  if (!target) throw new Error('no target: set it in qa/checks.json or pass --target');

  // The build under test, so a row in the table can be attributed to a commit
  // rather than to a date. Best-effort: a target that cannot answer is still
  // worth probing, it just records an unknown version.
  let version = 'unknown';
  try {
    const v = await fetch(target + '/api/version', { signal: AbortSignal.timeout(10000) });
    if (v.ok) version = (await v.json()).git_hash || 'unknown';
  } catch { /* recorded as unknown */ }

  const pages = (checks.pages || []).filter(wanted);
  const apis = (checks.api || []).filter(wanted);
  console.log(`qa: ${target} @ ${version}: ${pages.length} pages, ${apis.length} endpoints\n`);

  const browser = await chromium.launch();
  const observations = [];

  // Serial on purpose. These are latency numbers, and running six pages at
  // once against one host measures the contention rather than the pages.
  for (const check of pages) {
    const o = await probePage(browser, check, target);
    observations.push(o);
    console.log(`  ${glyph(o.status)} ${o.id.padEnd(20)} ready=${fmt(o.readyMs).padStart(7)} settled=${fmt(o.settledMs).padStart(7)} dom=${fmt(o.domInteractiveMs).padStart(6)} api=${String(o.apiRequests ?? '-').padStart(2)}  ${o.note}`);
  }
  await browser.close();

  for (const check of apis) {
    const o = await probeApi(check, target);
    observations.push(o);
    console.log(`  ${glyph(o.status)} ${o.id.padEnd(20)} ${fmt(o.ms)} ${o.note}`);
  }

  const run = {
    // ISO to the second, and the filename is derived from it, so runs sort
    // lexically and two runs in the same second would collide loudly rather
    // than silently overwrite.
    at: new Date().toISOString().replace(/\.\d+Z$/, 'Z'),
    target,
    version,
    observations,
  };

  const failed = observations.filter((o) => o.status === 'fail');
  const slow = observations.filter((o) => o.status === 'slow');
  console.log(`\n${observations.length} checks: ${observations.length - failed.length - slow.length} pass, ${slow.length} slow, ${failed.length} fail`);

  if (store) {
    mkdirSync(join(qaDir, 'results'), { recursive: true });
    const file = join(qaDir, 'results', run.at.replace(/[:]/g, '-') + '.json');
    writeFileSync(file, JSON.stringify(run, null, 2) + '\n');
    console.log(`stored ${file.replace(qaDir + '/', 'qa/')}`);
  }

  // Non-zero on a failure so this can gate something later if anyone wants it
  // to. Slow alone does not fail: the point of the table is to watch a number
  // move, and a budget nobody has tuned yet should not block anybody.
  process.exit(failed.length ? 1 : 0);
}

const glyph = (s) => (s === 'fail' ? 'FAIL' : s === 'slow' ? 'slow' : '  ok');
const fmt = (ms) => (ms === null || ms === undefined ? '-' : `${ms}ms`);

main().catch((err) => { console.error(err); process.exit(2); });
