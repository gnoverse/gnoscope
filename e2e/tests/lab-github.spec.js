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

// stubGitHub answers every /api/lab/github call with a small populated body
// and records the ?window= each windowed one asked for. /repos is not one:
// discovery has no window, a repository found is found.
async function stubGitHub(page, asked) {
  await page.route('**/api/lab/github/**', route => {
    const u = new URL(route.request().url());
    const name = u.pathname.split('/').pop();
    if (name !== 'repos') asked.push(u.searchParams.get('window'));
    const win = u.searchParams.get('window') || 'all';
    const bodies = {
      overview: {
        enabled: true, repos: 1, tracked: 1, discovered: 0, contributors: 1, prs: 1,
        by_kind: [], window: { label: win }, years: [2026, 2025, 2024],
        meta: { sync_finished: new Date().toISOString(), sync_seconds: '2.0' },
      },
      contributors: {
        enabled: true, window: { label: win }, new: [], scoring: { tiers: [], points: {} },
        top: [{ login: 'someone', score: 10, window_score: 3, merged_prs: 9, recent_merged: 3,
                reviews: 4, window_reviews: 1, comments: 2, window_comments: 0, commits: 12,
                repos: ['gnolang/gno'] }],
      },
      prs: { enabled: true, window: { label: win }, repos: [], recent: [] },
      repos: { enabled: true, repos: [], queries: [] },
    };
    return route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify(bodies[name] || {}),
    });
  });
}

// The windows are all time (the default) and one pill per year the data holds,
// in that order. Stubbed because the harness runs the section off, and a page
// that is off draws no pills: there is no data to window.
test('the window is all time by default, one pill per year, and the URL carries it', async ({ page }) => {
  const w = watch(page);
  const asked = [];
  await stubGitHub(page, asked);
  await page.goto('/lab/github');
  await settle(page);

  const pills = page.locator('#labgithub-content .gh-pill');
  await expect(pills).toHaveText(['all', '2026', '2025', '2024']);
  await expect(page.locator('#labgithub-content .gh-pill.on')).toHaveText('all');
  expect(asked.length).toBeGreaterThan(0);
  expect(asked.every(v => v === 'all')).toBe(true);
  await expect(page.locator('#labgithub-content th', { hasText: 'commits' })).toHaveCount(1);

  asked.length = 0;
  await pills.nth(2).click();
  await settle(page);
  await expect(page.locator('#labgithub-content .gh-pill.on')).toHaveText('2025');
  await expect(page).toHaveURL(/[?&]window=2025/);
  expect(asked.length).toBeGreaterThan(0);
  expect(asked.every(v => v === '2025')).toBe(true);
  // A year's table is that year's, not the all-time one relabelled, and it
  // has no commits column: GitHub does not date commits.
  await expect(page.locator('#labgithub-content th', { hasText: 'score in 2025' })).toHaveCount(1);
  await expect(page.locator('#labgithub-content th', { hasText: 'commits' })).toHaveCount(0);

  // Back to all time leaves no trace in the URL.
  await page.locator('#labgithub-content .gh-pill', { hasText: 'all' }).click();
  await settle(page);
  await expect(page).toHaveURL(/\/lab\/github$/);

  expect(w.jsErrors).toEqual([]);
});

test('a linked year opens on that year', async ({ page }) => {
  const w = watch(page);
  const asked = [];
  await stubGitHub(page, asked);
  await page.goto('/lab/github?window=2024');
  await settle(page);
  await expect(page.locator('#labgithub-content .gh-pill.on')).toHaveText('2024');
  expect(asked.length).toBeGreaterThan(0);
  expect(asked.every(v => v === '2024')).toBe(true);
  expect(w.jsErrors).toEqual([]);
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
