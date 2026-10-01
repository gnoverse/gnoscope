import { test, expect } from '@playwright/test';
import { watch, unexpected } from './helpers.js';

// The popup is a preview, and /search is the full answer.
//
// Seven groups (addresses, users, assets, realms, symbols, packages, and the
// "go to" shortcuts) at ten rows each are a scroll, not a popup. So the popup
// shows three of each, says how many it left out, and leads to /search, which
// lists every group in full and can be narrowed to one.
//
// `util0` is the fixture's twelve near-identical packages, which is the query
// that has more rows than the popup shows.

const POPUP_ROWS = 3;

async function type(page, text) {
  const input = page.locator('#search-input');
  await input.click();
  await input.fill(text);
  return input;
}

test('the popup shows a few rows per group and links to the rest', async ({ page }) => {
  const seen = watch(page);
  await page.goto('/');
  await type(page, 'util0');
  const results = page.locator('#search-results');
  await expect(results.locator('.search-all')).toBeVisible({ timeout: 15_000 });

  // Rows sit between their group label and the next label; count the
  // packages group's.
  const counts = await results.evaluate(box => {
    const out = {};
    let cur = null;
    for (const n of box.children) {
      if (n.classList.contains('search-section-label')) { cur = n.textContent; out[cur] = 0; }
      else if (n.classList.contains('search-result') && cur) out[cur]++;
    }
    return out;
  });
  for (const [label, n] of Object.entries(counts)) {
    expect(n, label).toBeLessThanOrEqual(POPUP_ROWS);
  }

  const more = results.locator('.search-more', { hasText: 'more packages' });
  await expect(more).toBeVisible();
  await more.click();
  await expect(page).toHaveURL(/\/search\?q=util0&kind=packages$/);

  // Narrowed to packages, and every one of them listed, not three.
  const groups = page.locator('#search-page-content .search-page-group');
  await expect(groups).toHaveCount(1);
  await expect(groups.first()).toHaveAttribute('data-kind', 'packages');
  expect(await groups.first().locator('.search-result').count()).toBeGreaterThan(POPUP_ROWS);
  await expect(page.locator('#search-page-content .gh-pill.on')).toContainText('packages');

  expect(unexpected(seen.failedRequests)).toEqual([]);
  expect(seen.jsErrors).toEqual([]);
});

test('enter opens the full results, and the strip narrows and widens them', async ({ page }) => {
  const seen = watch(page);
  await page.goto('/');
  const input = await type(page, 'hub');
  await expect(page.locator('#search-results .search-result').first()).toBeVisible({ timeout: 15_000 });
  await input.press('Enter');
  await expect(page).toHaveURL(/\/search\?q=hub$/);
  await expect(page.locator('#search-results')).not.toHaveClass(/open/);

  const content = page.locator('#search-page-content');
  await expect(content.locator('.section-title')).toHaveText('results for “hub”', { timeout: 15_000 });
  // The same groups, in the same order, as the popup.
  const kinds = await content.locator('.search-page-group').evaluateAll(gs => gs.map(g => g.dataset.kind));
  expect(kinds).toEqual(['users', 'assets', 'realms', 'packages']);
  // The box keeps the query, so refining starts from it.
  await expect(input).toHaveValue('hub');

  await content.locator('.gh-pill', { hasText: 'users' }).click();
  await expect(page).toHaveURL(/\/search\?q=hub&kind=users$/);
  await expect(content.locator('.search-page-group')).toHaveCount(1);
  await content.locator('.gh-pill', { hasText: /^all/ }).click();
  await expect(content.locator('.search-page-group')).toHaveCount(4);

  expect(unexpected(seen.failedRequests)).toEqual([]);
  expect(seen.jsErrors).toEqual([]);
});

test('enter on a complete address goes straight to it', async ({ page }) => {
  await page.goto('/');
  const addr = 'g1qyfled5ulf6wmu2u4smstn8rlzazanmqt0n8kh';
  const input = await type(page, addr);
  await input.press('Enter');
  await expect(page).toHaveURL(new RegExp('/address/' + addr));
});

test('/search/<query> is accepted and rewritten to the query form', async ({ page }) => {
  await page.goto('/search/hub');
  await expect(page).toHaveURL(/\/search\?q=hub$/);
  await expect(page.locator('#search-page-content .search-page-group').first()).toBeVisible({ timeout: 15_000 });
});

// With no network picked every source answers once per chain, and `moul` came
// back as two identical @moul rows, mainnet's and onyx's. One thing is one row,
// and it says which chains it is on.
test('one user registered on two chains is one row naming both', async ({ page }) => {
  await page.route('**/api/users/search*', route => route.fulfill({
    json: {
      network: '',
      users: [
        { network: 'alpha', name: 'twin', address: 'g1twin000000000000000000000000000000000' },
        { network: 'beta', name: 'twin', address: 'g1twin000000000000000000000000000000000' },
      ],
    },
  }));
  await page.goto('/');
  await type(page, 'twin');
  const rows = page.locator('#search-results .search-result', { hasText: '@twin' });
  await expect(rows.first()).toBeVisible({ timeout: 15_000 });
  await expect(rows).toHaveCount(1);
  await expect(rows.first().locator('.search-result-sub')).toContainText('alpha + beta');
});
