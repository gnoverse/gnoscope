import { test, expect } from '@playwright/test';
import { watch, unexpected } from './helpers.js';

// Address autocomplete. Typing part of an address used to answer "no results":
// the box recognised only a complete 40-character g1… by shape, and none of the
// four search endpoints matches an account that never deployed. Measured on
// mainnet 2026-10-01 with g1qyfled5ulf6wmu, the start of an account with 774
// transactions.
//
// The endpoint is stubbed rather than seeded. The harness has no RPC, so no
// balance is ever swept, and accounts.spec.js asserts exactly that emptiness;
// seeding `balances` here would make that spec lie.

const RICH = 'g1qyfled5ulf6wmu2u4smstn8rlzazanmqt0n8kh';
const POOR = 'g1qyzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzz';

function stubAddresses(page, calls) {
  return page.route('**/api/addresses/search*', route => {
    const q = new URL(route.request().url()).searchParams.get('q');
    calls.push(q);
    const all = [
      { network: 'alpha', address: RICH, ugnot: 709209683592, name: 'whale' },
      { network: 'alpha', address: POOR, ugnot: 1000000 },
    ];
    route.fulfill({
      json: { network: 'alpha', addresses: all.filter(a => a.address.startsWith(q)) },
    });
  });
}

async function type(page, text) {
  const input = page.locator('#search-input');
  await input.click();
  await input.fill(text);
}

test('a partial address suggests the accounts it could be, and opens one', async ({ page }) => {
  const seen = watch(page);
  const calls = [];
  await stubAddresses(page, calls);
  await page.goto('/');
  await type(page, 'g1qy');

  const results = page.locator('#search-results');
  await expect(results.locator('.search-section-label').first()).toHaveText('addresses', { timeout: 15_000 });
  const rows = results.locator('.search-result', { has: page.locator('.badge', { hasText: 'address' }) });
  await expect(rows).toHaveCount(2);

  // What is still to be typed is the part that stays bright; the typed part is
  // the dimmed prefix that gives way first.
  const first = rows.first();
  await expect(first.locator('.search-result-prefix')).toHaveText('g1qy');
  await expect(first.locator('.search-result-name')).toHaveText(RICH.slice(4));
  await expect(first.locator('.search-result-sub')).toContainText('@whale');
  await expect(first.locator('.search-result-sub')).toContainText('GNOT');

  await first.click();
  await expect(page).toHaveURL(new RegExp('/address/' + RICH));

  expect(unexpected(seen.failedRequests)).toEqual([]);
  expect(seen.jsErrors).toEqual([]);
});

test('a query that is not an address never asks for addresses', async ({ page }) => {
  const calls = [];
  await stubAddresses(page, calls);
  await page.goto('/');
  await type(page, 'hub');
  await expect(page.locator('#search-results .search-result').first()).toBeVisible({ timeout: 15_000 });
  await expect(page.locator('#search-results .search-section-label', { hasText: 'addresses' })).toHaveCount(0);
  expect(calls).toEqual([]);
});

test('a complete address is offered once, as the destination, not twice', async ({ page }) => {
  const calls = [];
  await stubAddresses(page, calls);
  await page.goto('/');
  await type(page, RICH);

  const results = page.locator('#search-results');
  await expect(results.locator('.search-section-label').first()).toHaveText('go to', { timeout: 15_000 });
  await expect.poll(() => calls.length).toBeGreaterThan(0);
  await expect(results.locator('.search-loading')).toHaveCount(0);
  await expect(results.locator('.search-section-label', { hasText: 'addresses' })).toHaveCount(0);
});
