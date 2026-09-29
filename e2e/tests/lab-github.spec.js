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

// The state the probe on val1 found: a token that 401s on every request leaves
// the section enabled, the tables empty and the stats bar showing eight zeros,
// which reads as "gno has no contributors" rather than "this token is wrong".
// The harness cannot produce that state (it runs the section off), so this
// drives the render directly against a stubbed response.
test('zero rows plus a reported problem leads with the cause, not the zeros', async ({ page }) => {
  const w = watch(page);
  await page.route('**/api/lab/github/**', route => route.fulfill({
    status: 200,
    contentType: 'application/json',
    body: JSON.stringify({
      enabled: true, repos: 0, tracked: 0, discovered: 0, contributors: 0, prs: 0,
      by_kind: [], window: { days: 30 },
      meta: { sync_finished: new Date().toISOString(), sync_seconds: '2.0', rate_core: '0/0',
              sync_problems: 'seed gnolang/gno: github /repos/gnolang/gno: 401 Bad credentials' },
      new: [], top: [], repos_: [], recent: [], queries: [],
    }),
  }));
  await page.goto('/lab/github');
  await settle(page);

  const panel = page.locator('#labgithub-content .gh-off');
  await expect(panel).toBeVisible();
  await expect(panel).toContainText('401');
  await expect(panel).toContainText('not a measurement');
  // The bar still draws, under the explanation rather than instead of it.
  await expect(page.locator('#labgithub-content .stats-bar')).toHaveCount(1);

  expect(w.jsErrors).toEqual([]);
});
