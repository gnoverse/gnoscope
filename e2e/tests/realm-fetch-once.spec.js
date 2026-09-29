import { expect, test } from '@playwright/test';

import { HUB, HUB_ROUTE } from '../harness/fixture.mjs';
import { settle, unexpected, watch } from './helpers.js';

// The realm page must not fetch the same thing twice.
//
// Its apiSWR call carries one required path and two optional ones, and a render
// runs again when a slow optional finally answers. That is correct for the
// header and the info table, which are drawn from those payloads. It is wrong
// for the calls, events, storage and deps tabs, which fetch their own data
// keyed on the realm path and care nothing for `inert` or `assets`: repainting
// used to re-issue all four of their requests for a byte-identical result.
//
// Measured on production 2026-09-29 before the fix: /api/deps twice per
// direction, /api/realm/usage twice, on a page whose inert read takes ten
// seconds and therefore guarantees the extra paint.
//
// Counting requests is the only assertion that catches this. Every earlier
// test on this page passed throughout, because a page that fetches everything
// twice renders exactly like one that fetches everything once.

const COUNTED = [
  '/api/realm/usage/',
  '/api/deps/',
  '/api/events/',
  '/api/storage/',
];

function countRequests(page) {
  const seen = [];
  page.on('request', (r) => {
    const u = new URL(r.url());
    if (COUNTED.some((p) => u.pathname.startsWith(p))) seen.push(u.pathname + u.search);
  });
  return seen;
}

test('a realm page load fetches each of its tab endpoints exactly once', async ({ page }) => {
  const w = watch(page);
  const seen = countRequests(page);

  // Delayed deliberately. The bug needs a slow optional path to show itself:
  // when inert answers promptly there is only one paint and the duplicates
  // never appear, which is how this survived every local run.
  await page.route('**/api/inert/package/**', async (route) => {
    await new Promise((r) => setTimeout(r, 1500));
    await route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({ meta: { path: HUB, status: 'inert', reason: 'awaiting a package approver' }, history: [] }),
    });
  });

  await page.goto(`/realm/${HUB_ROUTE}?network=alpha`);
  await settle(page);
  // The parked badge only exists after the slow path landed, so waiting for it
  // proves the second paint happened. Without that the test would pass by
  // never provoking the bug.
  await expect(page.locator('#realm-header .badge', { hasText: 'parked' })).toBeVisible({ timeout: 15_000 });
  await settle(page);

  const counts = new Map();
  for (const u of seen) counts.set(u, (counts.get(u) || 0) + 1);
  const duplicated = [...counts.entries()].filter(([, n]) => n > 1);

  expect(duplicated, 'each tab endpoint fetched exactly once per load').toEqual([]);

  expect(w.jsErrors, 'uncaught exceptions').toEqual([]);
  expect(unexpected(w.consoleErrors), 'console errors').toEqual([]);
});

test('the tabs still have their content after the second paint', async ({ page }) => {
  // The other half of the fix, and the way it could go wrong: the repaint no
  // longer clears these containers. If it cleared them without refetching, the
  // reader would be left looking at an empty tab, which is worse than the
  // duplicate request this removes.
  const w = watch(page);
  await page.route('**/api/inert/package/**', async (route) => {
    await new Promise((r) => setTimeout(r, 1500));
    await route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({ meta: { path: HUB, status: 'inert', reason: 'awaiting a package approver' }, history: [] }),
    });
  });

  await page.goto(`/realm/${HUB_ROUTE}?network=alpha&tab=deps`);
  await expect(page.locator('#realm-header .badge', { hasText: 'parked' })).toBeVisible({ timeout: 15_000 });
  await settle(page);

  // Drawn, and still drawn after the paint that the parked badge proves ran.
  // `.first()` because the legend under the graph carries 12px svg icons of
  // its own, so a bare `#dep-graph svg` is three elements and a strict-mode
  // violation rather than an assertion.
  await expect(page.locator('#dep-graph svg').first()).toBeVisible({ timeout: 20_000 });
  // The graph itself, not merely an svg element: nodes are what a reader came
  // for and what an emptied container would be missing.
  await expect(page.locator('#dep-graph svg circle').first()).toBeVisible({ timeout: 20_000 });
  await expect(page.locator('#tab-deps .skeleton')).toHaveCount(0);

  expect(w.jsErrors, 'uncaught exceptions').toEqual([]);
});
