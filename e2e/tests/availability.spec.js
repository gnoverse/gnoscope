import { expect, test } from '@playwright/test';

import { settle, unexpected, watch } from './helpers.js';

// The availability strip on /sanity: a week of history per network, rather
// than the one dot the page used to show.
//
// The three states worth looking at cannot be produced against a fixture
// (a chain does not wedge for two days on request), so they are stubbed, the
// same way the wedged diagnosis is in chainhealth.spec.js. The last test here
// is unstubbed and covers what the real probe actually writes.

const HOUR = 3600;

// strip builds `n` hourly cells ending now, with `bad` spans painted in.
function cells(n, bad = []) {
  const now = Date.now();
  const out = [];
  for (let i = n - 1; i >= 0; i--) {
    const start = new Date(now - i * HOUR * 1000).toISOString();
    const hit = bad.find(b => i >= b.from && i <= b.to);
    out.push(hit
      ? { start, state: hit.state, samples: 60, healthy: 0 }
      : { start, state: 'alive', samples: 60, healthy: 60 });
  }
  return out;
}

function payload(row) {
  return {
    enabled: true,
    window: '7d',
    windows: ['24h', '7d', '30d'],
    cell_seconds: HOUR,
    sample_seconds: 60,
    retention_days: 30,
    generated_at: new Date().toISOString(),
    rows: [row],
  };
}

async function stubHistory(page, body) {
  await page.route('**/api/health/history*', route =>
    route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(body) }));
}

test('a healthy week draws a full strip and claims no outage', async ({ page }) => {
  const seen = watch(page);
  await stubHistory(page, payload({
    network: 'alpha',
    cells: cells(168),
    samples: 10080,
    healthy_samples: 10080,
    uptime_percent: 100,
    outages: 0,
    down_seconds: 0,
    no_data_seconds: 0,
    current: { state: 'alive', healthy: true, start: new Date().toISOString(), end: new Date().toISOString(), seconds: 604800, samples: 10080, ongoing: true },
    incidents: [],
    summary: 'no outage in the last 7d; 100.00% of 10080 samples available',
  }));

  await page.goto('/sanity');
  await settle(page);

  const strip = page.locator('#sanity-availability');
  await expect(strip).toContainText('availability');
  await expect(strip).toContainText('no outage in the last 7d');
  await expect(strip).toContainText('100.00%');
  // One cell per bucket, all of them drawn.
  await expect(strip.locator('div[title*="samples healthy"]')).toHaveCount(168);

  expect(seen.jsErrors, 'uncaught exceptions').toEqual([]);
  expect(unexpected(seen.consoleErrors), 'console errors').toEqual([]);
});

test('a flapping chain lists its incidents with when and for how long', async ({ page }) => {
  const seen = watch(page);
  const ago = h => new Date(Date.now() - h * HOUR * 1000).toISOString();
  await stubHistory(page, payload({
    network: 'alpha',
    cells: cells(168, [
      { from: 3, to: 3, state: 'indexer_down' },
      { from: 20, to: 20, state: 'indexer_down' },
      { from: 44, to: 44, state: 'indexer_down' },
    ]),
    samples: 10080,
    healthy_samples: 10065,
    uptime_percent: 99.85,
    outages: 3,
    down_seconds: 900,
    no_data_seconds: 0,
    last_outage: { state: 'indexer_down', healthy: false, start: ago(3), end: ago(3), seconds: 300, samples: 5, ongoing: false },
    current: { state: 'alive', healthy: true, start: ago(3), end: new Date().toISOString(), seconds: 10800, samples: 180, ongoing: true },
    incidents: [
      { state: 'indexer_down', healthy: false, start: ago(3), end: ago(3), seconds: 300, samples: 5, ongoing: false, detail: 'our indexer is not answering' },
      { state: 'indexer_down', healthy: false, start: ago(20), end: ago(20), seconds: 300, samples: 5, ongoing: false },
      { state: 'indexer_down', healthy: false, start: ago(44), end: ago(44), seconds: 300, samples: 5, ongoing: false },
    ],
    summary: 'last unreachable 3h ago: indexer down for 5m; 3 outage(s) and 99.85% of 10080 samples available over the last 7d',
  }));

  await page.goto('/sanity');
  await settle(page);

  const strip = page.locator('#sanity-availability');
  // The sentence that was asked for: when, and for how long.
  await expect(strip).toContainText('last unreachable 3h ago');
  await expect(strip).toContainText('indexer down for 5m');
  // And the incidents under it, each with its own duration.
  await expect(strip.locator('[data-incident]')).toHaveCount(3);
  await expect(strip.locator('[data-incident]').first()).toContainText('for 5m');

  expect(seen.jsErrors, 'uncaught exceptions').toEqual([]);
  expect(unexpected(seen.consoleErrors), 'console errors').toEqual([]);
});

test('a long outage reads as ongoing, not as a blip', async ({ page }) => {
  const seen = watch(page);
  const start = new Date(Date.now() - 48 * HOUR * 1000).toISOString();
  await stubHistory(page, payload({
    network: 'alpha',
    cells: cells(168, [{ from: 0, to: 47, state: 'wedged' }]),
    samples: 10080,
    healthy_samples: 7200,
    uptime_percent: 71.43,
    outages: 1,
    down_seconds: 172800,
    no_data_seconds: 0,
    last_outage: { state: 'wedged', healthy: false, start, end: new Date().toISOString(), seconds: 172800, samples: 2880, ongoing: true },
    current: { state: 'wedged', healthy: false, start, end: new Date().toISOString(), seconds: 172800, samples: 2880, ongoing: true },
    incidents: [{ state: 'wedged', healthy: false, start, end: new Date().toISOString(), seconds: 172800, samples: 2880, ongoing: true, detail: 'consensus has not advanced for 2d' }],
    summary: 'wedged right now, for 2d so far; 1 outage(s) and 71.43% of 10080 samples available over the last 7d',
  }));

  await page.goto('/sanity');
  await settle(page);

  const strip = page.locator('#sanity-availability');
  await expect(strip).toContainText('wedged right now, for 2d so far');
  await expect(strip).toContainText('and counting');
  await expect(strip).toContainText('71.43%');

  expect(seen.jsErrors, 'uncaught exceptions').toEqual([]);
  expect(unexpected(seen.consoleErrors), 'console errors').toEqual([]);
});

// The fresh-instance case, which is what every new deployment looks like on
// its first day. It must not read as a green week it never observed, and it
// must not read as a red one either.
test('with no history the strip is blank and says so, with no percentage', async ({ page }) => {
  const seen = watch(page);
  const now = Date.now();
  await stubHistory(page, payload({
    network: 'alpha',
    cells: Array.from({ length: 168 }, (_, i) => ({
      start: new Date(now - (167 - i) * HOUR * 1000).toISOString(), samples: 0, healthy: 0,
    })),
    samples: 0,
    healthy_samples: 0,
    uptime_percent: 0,
    outages: 0,
    down_seconds: 0,
    no_data_seconds: 0,
    incidents: [],
    summary: 'no availability history recorded yet: this instance has taken no sample for this network',
  }));

  await page.goto('/sanity');
  await settle(page);

  const strip = page.locator('#sanity-availability');
  await expect(strip).toContainText('no availability history recorded yet');
  // No percentage at all, rather than 0.00% or 100.00%.
  await expect(strip).not.toContainText('0.00%');
  // Every cell drawn, every one of them the blank colour.
  await expect(strip.locator('div[title*="no sample in this bucket"]')).toHaveCount(168);

  expect(seen.jsErrors, 'uncaught exceptions').toEqual([]);
  expect(unexpected(seen.consoleErrors), 'console errors').toEqual([]);
});

// Unstubbed. The harness runs with -health-db, so opening the page writes a
// sample through the real probe and the strip draws what the real endpoint
// returned. This is the half the stubs above cannot cover: that the recording
// path, the query and the rendering agree.
test('the real endpoint records a sample and the strip draws it', async ({ page }) => {
  const seen = watch(page);

  await page.goto('/sanity');
  await settle(page);

  const strip = page.locator('#sanity-availability');
  await expect(strip).toContainText('availability');
  await expect(strip).toContainText('per cell');
  await expect(strip).toContainText('sampled every 1m');

  // The probe ran for this page load, so there is at least one sample and the
  // summary is a real sentence rather than the empty-state one.
  const data = await page.evaluate(async () => {
    const res = await fetch('/api/health/history?window=24h');
    return res.json();
  });
  expect(data.enabled, 'the harness runs with -health-db').toBe(true);
  expect(data.rows.length).toBeGreaterThan(0);
  expect(data.rows[0].samples, 'opening /sanity must record a sample').toBeGreaterThan(0);
  expect(data.rows[0].cells.length).toBe(144);

  // Switching the window is a second fetch and a repaint, and the generation
  // guard around it only breaks in a browser.
  await strip.getByRole('button', { name: '30d', exact: true }).click();
  await expect(strip).toContainText('4h per cell');

  expect(seen.jsErrors, 'uncaught exceptions').toEqual([]);
  expect(unexpected(seen.consoleErrors), 'console errors').toEqual([]);
});
