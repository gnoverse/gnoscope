import { expect, test } from '@playwright/test';

import { settle, unexpected, watch } from './helpers.js';

// What a visitor with no stored choice sees.
//
// The default was 'all networks', which sums every configured chain into one
// figure. On production that printed mainnet's 153,227 transactions plus
// onyx's 5,510 as 158,737, a number true of no chain. It was reported from
// outside (2026-10-15) as "I get different figures in two browsers": one
// profile had picked mainnet once, the other was on the default, and the page
// gave no sign that one of the two was a sum.
//
// The harness serves alpha and beta and no mainnet, so these also cover the
// fallback: the default is the first configured chain, not a hardcoded one.

test('a fresh visitor lands on one chain, not on the sum of every chain', async ({ page }) => {
  const seen = watch(page);
  await page.goto('/');
  await page.evaluate(() => localStorage.clear());
  await page.reload();
  await settle(page);

  const sel = page.locator('#network-select');
  await expect(sel).toHaveValue('alpha');

  // The tile agrees with the chain, and not with the blend. Asserting against
  // the API rather than a literal keeps this honest as the fixture grows.
  const single = await (await page.request.get('/api/stats?network=alpha')).json();
  const blended = await (await page.request.get('/api/stats')).json();
  expect(blended.total_txs,
    'the fixture must have two chains for this test to mean anything').toBeGreaterThan(single.total_txs);

  await expect(page.locator('#stat-txs')).toHaveText(single.total_txs.toLocaleString('en-US'));

  // And nothing claims a blend, because there is none.
  await expect(page.locator('#home-window-range .badge-blend')).toHaveCount(0);

  expect(seen.jsErrors).toEqual([]);
  expect(unexpected(seen.consoleErrors)).toEqual([]);
});

test('choosing all networks says so, and names the chains it is summing', async ({ page }) => {
  const seen = watch(page);
  await page.addInitScript(() => localStorage.setItem('gnoscope-network', 'all'));
  await page.goto('/');
  await settle(page);

  const blend = page.locator('#home-window-range .badge-blend');
  await expect(blend).toHaveCount(1);
  await expect(blend).toHaveText('summing alpha + beta');

  const blended = await (await page.request.get('/api/stats')).json();
  await expect(page.locator('#stat-txs')).toHaveText(blended.total_txs.toLocaleString('en-US'));

  expect(seen.jsErrors).toEqual([]);
  expect(unexpected(seen.consoleErrors)).toEqual([]);
});

// A stored choice is a choice: the new default must not overwrite it.
test('a chain picked earlier survives the new default', async ({ page }) => {
  await page.addInitScript(() => localStorage.setItem('gnoscope-network', 'beta'));
  await page.goto('/');
  await settle(page);

  await expect(page.locator('#network-select')).toHaveValue('beta');
});
