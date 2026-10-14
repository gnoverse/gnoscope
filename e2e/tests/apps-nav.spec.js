import { expect, test } from '@playwright/test';

import { settle, unexpected, watch } from './helpers.js';

// apps is its own section, and a category is a path.
//
// It used to be a child of contracts with the categories as a chip strip on
// the page, which meant the taxonomy did not exist until you had already
// landed on it. Three things have to hold for the move to be real, and none of
// them is visible to a Go test:
//
//   /apps/<category> renders    -> it is one view, not six, so route() has to
//                                  recognise the path and still mount #view-apps
//   the rail lights the child   -> the active entry is not the view id here,
//                                  which is the one case pageNav takes an
//                                  explicit id for
//   ?cat= still lands           -> every link sent before this keeps working

test('a category is a path, and the rail entry for it is the one that lights up', async ({ page }) => {
  const seen = watch(page);

  const res = await page.goto('/apps/content?network=alpha');
  expect(res.status()).toBe(200);
  await settle(page);

  await expect(page.locator('#nav-apps-content')).toHaveClass(/active/);
  await expect(page.locator('#nav-apps')).toHaveClass(/in-section/);

  // The section strip is the category navigation now, so it carries every
  // category and marks this one.
  const strip = page.locator('#view-apps main > .pagenav');
  await expect(strip.locator('a.active')).toHaveText('content');
  await expect(strip.locator('a')).toHaveCount(7); // "all" plus the six

  // And the page is actually filtered.
  const cards = page.locator('#apps-content .app-grid .app-card');
  expect(await cards.count()).toBeGreaterThan(0);
  for (const c of await cards.all()) {
    await expect(c.locator('.app-cat')).toHaveText('content');
  }

  expect(seen.jsErrors, 'uncaught exceptions').toEqual([]);
  expect(unexpected(seen.consoleErrors), 'console errors').toEqual([]);
});

test('a card\'s category is a link to its category page', async ({ page }) => {
  await page.goto('/apps?network=alpha');
  await settle(page);

  const chip = page.locator('#apps-content .app-grid .app-card .app-cat').first();
  const label = (await chip.textContent()).trim();
  await chip.click();

  await expect(page).toHaveURL(new RegExp('/apps/' + label + '(\\?|$)'));
  await settle(page);
  await expect(page.locator('#view-apps main > .pagenav a.active')).toHaveText(label);
});

test('the old ?cat= link still lands on the same page', async ({ page }) => {
  await page.goto('/apps?network=alpha&cat=content');
  await settle(page);

  const cards = page.locator('#apps-content .app-grid .app-card');
  expect(await cards.count()).toBeGreaterThan(0);
  for (const c of await cards.all()) {
    await expect(c.locator('.app-cat')).toHaveText('content');
  }
});

test('a category with nothing in it says so rather than drawing a blank', async ({ page }) => {
  await page.goto('/apps/governance?network=alpha');
  await settle(page);

  const content = page.locator('#apps-content');
  if (await content.locator('.app-grid .app-card').count() === 0) {
    await expect(content).toContainText('nothing here yet');
  }
});

// apps is out of contracts, and contracts keeps the three developer pages.
test('the contracts section no longer carries apps', async ({ page }) => {
  await page.goto('/contracts?network=alpha');
  await settle(page);
  await expect(page.locator('#nav-contracts .rail-label')).toHaveText('contracts');
  await expect(page.locator('.nav-group', { has: page.locator('#nav-contracts') })
    .locator('.nav-kids a')).toHaveCount(2);
});
