import { expect, test } from '@playwright/test';

import { settle } from './helpers.js';

// The 2026-09-28 rename, from the reader's side.
//
// Two things here are invisible until they are wrong, and neither shows up in a
// test that only checks the page renders:
//
//   - The localStorage keys moved from `mygnoscan-*` to `gnoscope-*`. Renaming
//     a persistence key is silent data loss: nobody sees an error, they just
//     find their chain back on "all networks", and cannot tell a rename from a
//     bug. pkg/web's TestEveryPersistedSettingIsMigratedFromTheOldName keeps the
//     migration list complete; this checks the migration actually runs, in a
//     browser, before first paint.
//   - The notice has to appear once and then stay gone. A banner that comes back
//     on every load is worse than no banner.

test('settings saved under the old name survive the rename', async ({ page }) => {
  await page.goto('/');
  await page.evaluate(() => {
    localStorage.clear();
    localStorage.setItem('mygnoscan-network', 'mainnet');
    localStorage.setItem('mygnoscan-rail', 'collapsed');
  });
  await page.reload();
  await settle(page);

  const after = await page.evaluate(() => ({
    network: localStorage.getItem('gnoscope-network'),
    rail: localStorage.getItem('gnoscope-rail'),
    oldNetwork: localStorage.getItem('mygnoscan-network'),
    oldRail: localStorage.getItem('mygnoscan-rail'),
  }));
  expect(after.network).toBe('mainnet');
  expect(after.rail).toBe('collapsed');
  // Moved, not copied: leaving the old keys behind means the migration runs
  // again on every load and would undo a later change to the same setting.
  expect(after.oldNetwork).toBeNull();
  expect(after.oldRail).toBeNull();

  // Applied before paint, which is the whole reason the migration sits in the
  // pre-paint script rather than in the app's startup.
  await expect(page.locator('html')).toHaveClass(/rail-collapsed/);
});

test('a choice made since the rename is not overwritten by the old key', async ({ page }) => {
  await page.goto('/');
  await page.evaluate(() => {
    localStorage.clear();
    localStorage.setItem('mygnoscan-network', 'mainnet');
    localStorage.setItem('gnoscope-network', 'test5');
  });
  await page.reload();
  await settle(page);

  expect(await page.evaluate(() => localStorage.getItem('gnoscope-network'))).toBe('test5');
  expect(await page.evaluate(() => localStorage.getItem('mygnoscan-network'))).toBeNull();
});

test('the rename notice shows once and stays dismissed', async ({ page }) => {
  await page.goto('/');
  await page.evaluate(() => localStorage.clear());
  await page.reload();
  await settle(page);

  const notice = page.locator('#rename-notice');
  await expect(notice).toBeVisible();
  await expect(notice).toContainText('mygnoscan is now gnoscope');

  await notice.getByRole('button').click();
  await expect(notice).toBeHidden();

  await page.reload();
  await settle(page);
  await expect(notice).toBeHidden();
});
