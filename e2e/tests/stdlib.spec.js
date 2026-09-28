import { expect, test } from '@playwright/test';

import { settle, unexpected, watch } from './helpers.js';

// The standard library pages.
//
// The harness has no RPC (see EXPECTED_FAILURES), and stdlib can only come
// from a node, so nothing is crawled here. That makes this the degraded path,
// which is the one worth pinning: a search hit that opens a blank page is
// worse than one that is not listed, and "no stdlib on this instance" has to
// read as an answer rather than as a broken page.

test('a stdlib package page says so when the package is not held', async ({ page }) => {
  const seen = watch(page);

  await page.goto('/stdlib/strings?network=alpha');
  await settle(page);

  const panel = page.locator('#stdlib-content');
  await expect(page.locator('#view-stdlib')).toBeVisible();
  await expect(panel).not.toBeEmpty();
  await expect(panel).toContainText(/not a standard library package|stdlib/i);

  expect(seen.jsErrors).toEqual([]);
  expect(unexpected(seen.failedRequests)).toEqual([]);
});

// A nested stdlib path has slashes in it (`chain/banker`, `crypto/sha256`), so
// the route has to take the whole tail rather than one segment. Getting this
// wrong sends two thirds of the standard library to the home page.
test('a nested stdlib path routes to the stdlib view, not home', async ({ page }) => {
  const seen = watch(page);

  await page.goto('/stdlib/crypto/sha256?network=alpha');
  await settle(page);

  await expect(page.locator('#view-stdlib')).toBeVisible();
  await expect(page.locator('#view-home')).not.toBeVisible();
  await expect(page).toHaveURL(/\/stdlib\/crypto\/sha256/);

  expect(seen.jsErrors).toEqual([]);
});
