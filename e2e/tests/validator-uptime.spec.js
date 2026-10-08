import { expect, test } from '@playwright/test';

import { settle, watch } from './helpers.js';

// The uptime column on /validators: blocks signed of the last N, read from each
// block's commit. A validator that missed blocks says how many; one that did
// not says 100% quietly; and a node that cannot answer leaves a dash, not a 0%.
const SET = [
  { address: 'g1val_a', voting_power: '60' },
  { address: 'g1val_b', voting_power: '40' },
];

async function routeSet(page, uptime) {
  await page.route('**/api/validators/live*', route =>
    route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(SET) }));
  await page.route('**/api/validators/uptime*', route => uptime
    ? route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(uptime) })
    : route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ network: 'alpha', requested: 100, read: 0, validators: [], note: 'no rpc' }) }));
}

test('the uptime column shows signed share and the misses', async ({ page }) => {
  const seen = watch(page);
  await routeSet(page, {
    network: 'alpha', requested: 100, read: 100,
    validators: [
      { address: 'g1val_b', signed: 90, missed: 10, uptime: 0.9 },
      { address: 'g1val_a', signed: 100, missed: 0, uptime: 1 },
    ],
  });
  await page.goto('/validators?network=alpha');
  await settle(page);

  const rows = page.locator('#valset tbody tr');
  await expect(rows).toHaveCount(2);
  const a = rows.filter({ hasText: 'g1val_a' }).first();
  const b = rows.filter({ hasText: 'g1val_b' }).first();
  await expect(a).toContainText('100%');
  await expect(b).toContainText('90.0%');
  await expect(b).toContainText('-10');
  expect(seen.jsErrors).toEqual([]);
});

test('a node that cannot answer leaves a dash, not a zero', async ({ page }) => {
  const seen = watch(page);
  await routeSet(page, null);
  await page.goto('/validators?network=alpha');
  await settle(page);

  const rows = page.locator('#valset tbody tr');
  await expect(rows).toHaveCount(2);
  await expect(page.locator('#valset td[title*="signed"]')).toHaveCount(0);
  await expect(page.locator('#valset thead')).toContainText('uptime');
  expect(seen.jsErrors).toEqual([]);
});
