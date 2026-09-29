// /lab/github with the section switched off, which is how the suite runs it
// and how most instances run it: the harness starts the binary without
// -github-db, so there is no store and no token.
//
// That is the state worth pinning here rather than an inconvenience. A
// section that is off has to *say* it is off, in a page that draws cleanly
// and throws nothing; the failure it replaces is an empty page with a red
// console, which reads as a bug and gets reported as one. The populated
// state is covered by Go tests in pkg/ghlab, against a local HTTP server, so
// nothing in the suite has to hold a GitHub token.
import { test, expect } from '@playwright/test';

import { settle, unexpected, watch } from './helpers.js';

test('the lab index offers github beside gnohub', async ({ page }) => {
  const w = watch(page);
  await page.goto('/lab');
  await settle(page);

  // Located by name rather than by index. The lab index gains cards, and a
  // test that says "the second one" starts asserting about whichever surface
  // happened to be added last: this one broke the day cartography landed
  // ahead of it, with a failure that said nothing about github.
  const card = page.locator('.gh-lab-card', { hasText: 'github' });
  await expect(card).toHaveCount(1);
  await expect(card.locator('.gh-lab-card-title')).toContainText('github');
  await expect(page.locator('.gh-lab-card', { hasText: 'gnohub' })).toHaveCount(1);

  await card.locator('.gh-lab-card-foot a').click();
  await settle(page);
  await expect(page).toHaveURL(/\/lab\/github$/);
  expect(w.jsErrors).toEqual([]);
  expect(unexpected(w.failedRequests)).toEqual([]);
});

test('with no database the page says so instead of erroring', async ({ page }) => {
  const w = watch(page);
  await page.goto('/lab/github');
  await settle(page);

  const off = page.locator('.gh-off');
  await expect(off).toBeVisible();
  await expect(off).toContainText('off on this instance');
  // The reason, not just the fact. An operator reading this has to learn
  // which of the two switches is missing without opening the source.
  await expect(off).toContainText('-github-db');

  // No half-drawn page behind the notice: a disabled section that also paints
  // an empty stats bar and five empty tables is worse than either state.
  await expect(page.locator('#labgithub-content .stats-bar')).toHaveCount(0);
  await expect(page.locator('#labgithub-content table')).toHaveCount(0);

  expect(w.jsErrors).toEqual([]);
  expect(unexpected(w.failedRequests)).toEqual([]);
});

test('the window pills are there and switching one does not throw', async ({ page }) => {
  const w = watch(page);
  await page.goto('/lab/github');
  await settle(page);

  const pills = page.locator('.gh-pill');
  await expect(pills).toHaveCount(3);
  await expect(pills.nth(1)).toHaveClass(/on/);

  await pills.nth(2).click();
  await settle(page);
  await expect(page.locator('.gh-pill.on')).toHaveText('90 days');
  await expect(page.locator('.gh-off')).toBeVisible();

  expect(w.jsErrors).toEqual([]);
  expect(unexpected(w.failedRequests)).toEqual([]);
});

test('the rail entry routes and lights up', async ({ page }) => {
  const w = watch(page);
  await page.goto('/');
  await settle(page);

  await page.locator('#nav-labgithub').click();
  await settle(page);
  await expect(page).toHaveURL(/\/lab\/github$/);
  await expect(page.locator('#nav-labgithub')).toHaveClass(/active/);
  // The section strip, which only appears when NAV and the rail agree about
  // this entry belonging to /lab. Scoped to the visible one: every view a
  // reader has opened keeps its own strip in the DOM, hidden, so an unscoped
  // .pagenav matches the home strip too.
  await expect(page.locator('.pagenav:visible')).toContainText('github');

  expect(w.jsErrors).toEqual([]);
  expect(unexpected(w.failedRequests)).toEqual([]);
});
