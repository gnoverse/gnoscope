// The table. Reads every run under qa/results/ and pivots them into one grid:
// a row per check, a column per run, newest on the right.
//
// The whole point of the storage format is here. Each run stores a flat list
// of observations keyed by check id, never a fixed set of columns, so the
// check list can change without invalidating history. A check added last week
// is blank in the runs before it existed; a check deleted today keeps its
// history and simply stops gaining columns. Neither case needs a migration,
// and neither case silently drops a number.
//
//   node qa/report.mjs                  the last 8 runs, readyMs
//   node qa/report.mjs --runs 20        more history
//   node qa/report.mjs --metric domInteractiveMs
//   node qa/report.mjs --markdown       for pasting into an issue
//   node qa/report.mjs --only api       just the endpoints

import { readdirSync, readFileSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

const qaDir = dirname(fileURLToPath(import.meta.url));
const resultsDir = join(qaDir, 'results');

const argv = process.argv.slice(2);
const flag = (name, fallback) => {
  const i = argv.indexOf(`--${name}`);
  return i === -1 ? fallback : argv[i + 1];
};
const maxRuns = Number(flag('runs', 8));
const metric = flag('metric', null);
const onlyKind = flag('only', null);
const markdown = argv.includes('--markdown');

let files;
try {
  files = readdirSync(resultsDir).filter((f) => f.endsWith('.json')).sort();
} catch {
  console.error('no qa/results/ yet, run `node qa/run.mjs` first');
  process.exit(1);
}
if (!files.length) {
  console.error('no runs recorded yet, run `node qa/run.mjs` first');
  process.exit(1);
}

const runs = files.slice(-maxRuns).map((f) => JSON.parse(readFileSync(join(resultsDir, f), 'utf8')));

// The union across the window, not the intersection: a check that only exists
// in the newest run still gets a row, and so does one that was retired three
// runs ago. Ordered by kind then id so pages and endpoints do not interleave.
const seen = new Map();
for (const run of runs) {
  for (const o of run.observations) {
    if (onlyKind && o.kind !== onlyKind) continue;
    if (!seen.has(o.id)) seen.set(o.id, o.kind);
  }
}
const ids = [...seen.entries()]
  .sort((a, b) => (a[1] === b[1] ? a[0].localeCompare(b[0]) : a[1].localeCompare(b[1])))
  .map(([id]) => id);

// Each kind has a default metric: what "slow" means for a page is how long
// until it drew, and for an endpoint it is how long it took to answer.
const metricFor = (kind) => metric || (kind === 'page' ? 'readyMs' : 'ms');

const lookup = (run, id) => run.observations.find((o) => o.id === id);

function cell(run, id) {
  const o = lookup(run, id);
  if (!o) return { text: '', status: 'absent' };
  const v = o[metricFor(o.kind)];
  if (o.status === 'fail') return { text: 'FAIL', status: 'fail' };
  if (v === null || v === undefined) return { text: '?', status: o.status };
  return { text: String(v), status: o.status };
}

// Column headers carry the version rather than the full timestamp: which
// commit a number belongs to is the question anyone reading a regression asks
// first, and a date cannot answer it. Two runs against the same build are
// common though (that is how you tell a flaky check from a real one), so a
// repeated version falls back to the clock rather than printing the same
// header twice.
const versionCounts = new Map();
for (const r of runs) versionCounts.set(r.version, (versionCounts.get(r.version) || 0) + 1);
const header = runs.map((r) => {
  const v = r.version && r.version !== 'unknown' ? r.version : null;
  if (!v) return r.at.slice(5, 16);
  return versionCounts.get(r.version) > 1 ? `${v}/${r.at.slice(11, 16)}` : v;
});

const rows = ids.map((id) => {
  const cells = runs.map((r) => cell(r, id));
  return { id, kind: seen.get(id), cells };
});

// A trailing marker for what changed between the last two runs that both have
// a number, so a regression is visible without reading across the row. Only
// comparable observations count: a check that was absent is not an improvement.
//
// Two floors, and both are needed. A percentage alone makes 45ms to 59ms look
// like a 31% regression, which on a page that is an eyeblink either way is
// noise that trains the reader to ignore the column. An absolute delta alone
// would stay silent on an endpoint going from 200ms to 400ms. Something has to
// clear both bars before it is worth printing.
const MIN_PCT = 20;
const MIN_ABS_MS = 40;

function delta(cells) {
  const nums = cells.map((c) => (/^\d+$/.test(c.text) ? Number(c.text) : null));
  const present = nums.filter((n) => n !== null);
  if (present.length < 2) return '';
  const prev = present[present.length - 2];
  const last = present[present.length - 1];
  if (prev === 0) return '';
  const pct = Math.round(((last - prev) / prev) * 100);
  if (Math.abs(pct) < MIN_PCT || Math.abs(last - prev) < MIN_ABS_MS) return '';
  return pct > 0 ? `+${pct}%` : `${pct}%`;
}

const widths = header.map((h, i) =>
  Math.max(h.length, ...rows.map((r) => r.cells[i].text.length)));
const idWidth = Math.max(5, ...rows.map((r) => r.id.length));

if (markdown) {
  console.log(`\`${runs[0].target}\` · metric: readyMs for pages, ms for endpoints · blank = the check did not exist in that run\n`);
  console.log('| check | ' + header.join(' | ') + ' | trend |');
  console.log('|---|' + header.map(() => '---:').join('|') + '|---|');
  for (const r of rows) {
    console.log(`| \`${r.id}\` | ` + r.cells.map((c) => c.text || '').join(' | ') + ` | ${delta(r.cells)} |`);
  }
} else {
  console.log(`\n${runs[0].target} · ${runs.length} run(s) · pages in readyMs, endpoints in ms · blank = check absent that run\n`);
  console.log('  ' + 'check'.padEnd(idWidth) + '  ' + header.map((h, i) => h.padStart(widths[i])).join('  ') + '   trend');
  console.log('  ' + '-'.repeat(idWidth) + '  ' + widths.map((w) => '-'.repeat(w)).join('  ') + '   -----');
  let kind = null;
  for (const r of rows) {
    if (r.kind !== kind) { kind = r.kind; console.log(`  [${kind}]`); }
    const marks = r.cells.map((c, i) => c.text.padStart(widths[i]));
    const anyBad = r.cells.some((c) => c.status === 'fail') ? ' !'
      : r.cells[r.cells.length - 1].status === 'slow' ? ' ~' : '  ';
    console.log(`${anyBad}${r.id.padEnd(idWidth)}  ${marks.join('  ')}   ${delta(r.cells)}`);
  }
  console.log('\n  ! a run in this window failed the check   ~ over budget in the newest run');
  const last = runs[runs.length - 1];
  const f = last.observations.filter((o) => o.status === 'fail');
  const s = last.observations.filter((o) => o.status === 'slow');
  console.log(`\n  newest: ${last.at} @ ${last.version}: ${f.length} fail, ${s.length} slow`);
  if (f.length) for (const o of f) console.log(`    FAIL ${o.id}: ${o.note}`);
}
