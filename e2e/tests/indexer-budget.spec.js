import { expect, test } from '@playwright/test';

import { settle, unexpected, watch } from './helpers.js';

// The indexer budget section on /sanity: what this process spends upstream,
// against the one per-IP rule the public indexers sit behind.
//
// The readings that matter cannot be produced against the harness (the fake
// indexer is on localhost, which is not behind anybody's Cloudflare edge), so
// the interesting states are stubbed the same way the availability strip's are.
// The last test is unstubbed and covers the half a stub cannot: that the meter
// is actually wired to the client, and that opening a page is enough to make it
// count something.

const SHARED = 'https://indexer.onyx.testnets.gno.land/graphql/query';

function usage(over) {
  const shared = {
    asked: 0, sent: 0, collapsed: 0, refused: 0, failed: 0, bytes: 0,
    collapsed_percent: 0,
    last_60s: 0, last_300s: 0, last_1h: 0,
    per_sec_60s: 0, per_sec_300s: 0, per_sec_1h: 0,
    peak_minute: 0, peak_minute_per_sec: 0, peak_300s: 0,
    current_percent: 0, peak_percent: 0,
    verdict: 'under', current_verdict: 'under',
    ...(over.shared || {}),
  };
  return {
    generated_at: new Date().toISOString(),
    since: new Date(Date.now() - 3600_000).toISOString(),
    uptime_seconds: 3600,
    budget: { requests: 3100, seconds: 300, per_second: 10.33, note: 'one Cloudflare rule, per IP' },
    shared,
    all: { ...shared, ...(over.all || {}) },
    endpoints: over.endpoints || [],
    ttl_seconds: [5, 30, 300],
    bounds: {
      max_endpoints: 16, max_groups_per_endpoint: 48,
      second_buckets_per_endpoint: 300, minute_buckets_per_endpoint: 1440,
      cache_sim_keys: 8192, cache_sim_tracked: 120, cache_sim_evictions: 0,
      note: 'fixed memory',
    },
    summary: over.summary || 'under the budget',
  };
}

function endpoint(over) {
  return {
    url: SHARED,
    shared_bucket: true,
    asked: 0, sent: 0, collapsed: 0, refused: 0, failed: 0, bytes: 0,
    collapsed_percent: 0,
    last_60s: 0, last_300s: 0, last_1h: 0,
    per_sec_60s: 0, per_sec_300s: 0, per_sec_1h: 0,
    peak_minute: 0, peak_minute_per_sec: 0, peak_300s: 0,
    current_percent: 0, peak_percent: 0,
    verdict: 'under', current_verdict: 'under',
    groups: [],
    ...over,
  };
}

async function stubUsage(page, body) {
  await page.route('**/api/indexer/usage*', route =>
    route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(body) }));
}

test('a quiet instance reads as under the budget', async ({ page }) => {
  const seen = watch(page);
  await stubUsage(page, usage({
    shared: {
      asked: 900, sent: 300, collapsed: 600, collapsed_percent: 66.67,
      last_300s: 120, per_sec_300s: 0.4, peak_300s: 210, peak_minute: 90,
      current_percent: 3.87, peak_percent: 6.77,
    },
    endpoints: [endpoint({
      sent: 300, peak_300s: 210, per_sec_300s: 0.4,
      groups: [{
        label: 'getTransactions', asked: 700, sent: 220, collapsed: 480,
        refused: 0, failed: 0, bytes: 1_400_000, sent_percent: 73.33,
        would_hit: [10, 40, 150], would_hit_percent: [4.55, 18.18, 68.18],
      }],
    })],
    summary: 'under the budget: 120 requests in the last 300s against a ceiling of 3100 (4%)',
  }));

  await page.goto('/sanity');
  await settle(page);

  const sec = page.locator('#sanity-indexer-budget');
  await expect(sec).toContainText('indexer budget');
  await expect(sec).toContainText('3,100 / 300s');
  await expect(sec).toContainText('under');
  await expect(sec).toContainText('getTransactions');
  // The would-have-hit columns, which are the answer this section exists to
  // carry: a cache at 300s would have removed two thirds of the top caller.
  await expect(sec).toContainText('would hit 300s');
  await expect(sec).toContainText('150 (68%)');
  await expect(sec).toContainText('on the shared per-IP budget');

  expect(seen.jsErrors, 'uncaught exceptions').toEqual([]);
  expect(unexpected(seen.consoleErrors), 'console errors').toEqual([]);
});

test('a burst over the ceiling says so at a glance', async ({ page }) => {
  const seen = watch(page);
  await stubUsage(page, usage({
    shared: {
      asked: 9000, sent: 8200, collapsed: 800, collapsed_percent: 8.89, refused: 277,
      last_300s: 3400, per_sec_300s: 11.33, peak_300s: 4100, peak_minute: 1200,
      current_percent: 109.68, peak_percent: 132.26,
      verdict: 'over', current_verdict: 'over',
    },
    endpoints: [endpoint({
      sent: 8200, refused: 277, peak_300s: 4100,
      groups: [{
        label: 'getTransactions', asked: 8000, sent: 7800, collapsed: 200,
        refused: 277, failed: 0, bytes: 90_000_000, sent_percent: 95.12,
        would_hit: [400, 2000, 6000], would_hit_percent: [5.13, 25.64, 76.92],
      }],
    })],
    summary: 'over the budget: 3400 requests in the last 300s against a ceiling of 3100 (110%)',
  }));

  await page.goto('/sanity');
  await settle(page);

  const sec = page.locator('#sanity-indexer-budget');
  await expect(sec).toContainText('over');
  await expect(sec).toContainText('110% of budget');
  await expect(sec).toContainText('132% of budget');
  await expect(sec).toContainText('277');

  expect(seen.jsErrors, 'uncaught exceptions').toEqual([]);
  expect(unexpected(seen.consoleErrors), 'console errors').toEqual([]);
});

// Unstubbed. The harness reads a fake indexer on localhost, which is not behind
// the shared edge, so the budget figures are legitimately zero here and the
// section has to say that rather than reporting a number it did not measure.
// What this does cover, and nothing else does, is that the meter is wired to
// the client at all: serving any page makes GraphQL requests, and they have to
// turn up here.
test('the real meter counts the requests this instance made', async ({ page }) => {
  const seen = watch(page);

  await page.goto('/sanity');
  await settle(page);

  const data = await page.evaluate(async () => {
    const res = await fetch('/api/indexer/usage');
    return res.json();
  });

  expect(data.all.sent, 'serving pages must have made outbound requests').toBeGreaterThan(0);
  expect(data.endpoints.length).toBeGreaterThan(0);
  expect(data.endpoints[0].groups.length, 'every request lands in a group').toBeGreaterThan(0);
  // The fake indexer is on localhost, so nothing here spends the shared budget.
  expect(data.endpoints[0].shared_bucket).toBe(false);
  expect(data.shared.sent).toBe(0);
  expect(data.summary).toContain('none of them to an endpoint on the shared per-IP budget');
  // The labels are read from the query text this repo writes, so they are
  // GraphQL root fields rather than "unknown". /sanity with -sync=false asks
  // for the tip and block 1, so those are the two that turn up.
  const labels = data.endpoints.flatMap(e => e.groups.map(g => g.label));
  expect(labels).toContain('latestBlockHeight');
  expect(labels).not.toContain('unknown');

  const sec = page.locator('#sanity-indexer-budget');
  await expect(sec).toContainText('indexer budget');
  await expect(sec).toContainText('own origin, off the shared budget');
  await expect(sec).toContainText('counting since');

  expect(seen.jsErrors, 'uncaught exceptions').toEqual([]);
  expect(unexpected(seen.consoleErrors), 'console errors').toEqual([]);
});
