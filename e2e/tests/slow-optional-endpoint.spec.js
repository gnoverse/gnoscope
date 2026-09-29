import { expect, test } from '@playwright/test';

import { HUB, HUB_ROUTE } from '../harness/fixture.mjs';
import { unexpected, watch } from './helpers.js';

// A slow best-effort endpoint must not hold a page.
//
// The realm page asks for three things at once: the package itself, its
// inert-lifecycle status, and the GRC20s it issues. Only the first is required;
// the other two are declared optional in apiSWR because a realm that was never
// parked and issues no token has nothing there, which is the common case.
//
// apiSWR used to Promise.all the three anyway, so the page painted at the speed
// of the slowest of them. Measured against production on 2026-09-29, a cold
// /api/inert/package took 34s and the realm page showed nothing at all for
// those 34s — no header, no tab strip, no source, none of which that endpoint
// has anything to do with.
//
// Now the required path paints on its own and the optional half fills in when
// it lands. The delay here is deliberately longer than the assertions below
// would tolerate, so a regression cannot pass by being merely fast.
const STALL_MS = 8000;

test('a stalled optional endpoint does not hold the realm page', async ({ page }) => {
  const seen = watch(page);

  let released = null;
  await page.route('**/api/inert/package/**', async (route) => {
    released = Date.now();
    await new Promise(r => setTimeout(r, STALL_MS));
    await route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({ meta: { path: HUB, status: 'inert', reason: 'awaiting a package approver' }, history: [] }),
    });
  });

  const started = Date.now();
  await page.goto(`/realm/${HUB_ROUTE}?network=alpha`);

  // The header, the tab strip and the info table are all drawn from the
  // required payload, so all three have to be on screen well before the
  // stalled endpoint answers.
  await expect(page.locator('#realm-header h2')).toHaveText(HUB, { timeout: 3000 });
  await expect(page.locator('#realm-tabs .tab[data-tab="source"]')).toBeVisible({ timeout: 3000 });
  await expect(page.locator('#tab-info table')).toBeVisible({ timeout: 3000 });

  const painted = Date.now() - started;
  expect(released, 'the stalled endpoint was actually requested').not.toBeNull();
  expect(painted, 'painted before the optional endpoint answered').toBeLessThan(STALL_MS);

  // And when it does land, the badge it is responsible for turns up, on the
  // same page, without a reload.
  await expect(page.locator('#realm-header .badge', { hasText: 'parked' }))
    .toBeVisible({ timeout: STALL_MS + 5000 });

  expect(seen.jsErrors, 'uncaught exceptions').toEqual([]);
  expect(unexpected(seen.consoleErrors), 'console errors').toEqual([]);
});
