// gnohub: the experimental /lab surface that reads a realm as a repository.
//
// It reuses /api/realm and /api/packages and adds one endpoint of its own
// (/api/realm/deploys), so what these cover is the half no Go test can: the
// route grammar, the file browser, and that none of it throws.
import { test, expect } from '@playwright/test';

import { DEPENDENTS, HUB, HUB_ROUTE, HUB_CREATOR, SHARED_PACKAGES } from '../harness/fixture.mjs';
import { settle, unexpected, watch } from './helpers.js';

const NET = '?network=alpha';

test('the lab index offers gnohub and nothing else claims to be finished', async ({ page }) => {
  const w = watch(page);
  await page.goto('/lab' + NET);
  await settle(page);

  // The index carries more than one card now, so the assertions are scoped to
  // gnohub's, except the badge, which is the half of this test's name that is
  // about the whole page: every card here has to say experimental, and a new
  // surface that forgot to is exactly what this should catch.
  const cards = page.locator('.gh-lab-card');
  const n = await cards.count();
  expect(n).toBeGreaterThan(0);
  await expect(cards.locator('.gh-lab-card-title .badge')).toHaveText(Array(n).fill('experimental'));

  const hub = cards.filter({ hasText: 'gnohub' });
  await expect(hub.locator('.gh-lab-card-title')).toContainText('gnohub');
  await hub.locator('.gh-lab-card-foot a').click();
  await expect(page).toHaveURL(/\/gnohub/);
  expect(w.jsErrors).toEqual([]);
  expect(unexpected(w.failedRequests)).toEqual([]);
});

test('the hub lists owners and the newest repos, and an owner tile opens that owner', async ({ page }) => {
  const w = watch(page);
  await page.goto('/gnohub' + NET);
  await settle(page);

  const owners = page.locator('.gh-owner-tile');
  expect(await owners.count()).toBeGreaterThan(0);
  await expect(page.locator('.gh-repo-row').first()).toBeVisible();

  await page.locator('.gh-owner-tile', { hasText: 'hub' }).first().click();
  await settle(page);
  await expect(page).toHaveURL(/\/gnohub\/hub/);
  await expect(page.locator('.gh-owner-title')).toHaveText('hub');
  // The owner's address is counted from what it deployed, not asserted from
  // the namespace, so it has to actually appear.
  await expect(page.locator('.gh-owner-sub').first()).toContainText('deployed by');
  expect(w.jsErrors).toEqual([]);
  expect(unexpected(w.failedRequests)).toEqual([]);
});

test('a repo page carries the path, the deploy, the files and a way back to the realm page', async ({ page }) => {
  const w = watch(page);
  await page.goto('/gnohub/' + HUB_ROUTE + NET);
  await settle(page);

  await expect(page.locator('.gh-repo-title-path')).toHaveText(HUB);
  await expect(page.locator('.gh-repo-facts')).toContainText('deployed');
  await expect(page.locator('.gh-file-row')).toHaveCount(1);
  await expect(page.locator('.gh-file-name')).toContainText('core.gno');

  // The breadcrumb owner is a link to the owner page, which is most of what
  // makes the github shape navigable at all.
  await page.locator('.gh-crumbs a', { hasText: /^hub$/ }).click();
  await settle(page);
  await expect(page).toHaveURL(/\/gnohub\/hub/);

  await page.goBack();
  await settle(page);
  await page.locator('.gh-tab-out').click();
  await settle(page);
  await expect(page).toHaveURL(new RegExp('/realm/' + HUB_ROUTE));
  expect(w.jsErrors).toEqual([]);
  expect(unexpected(w.failedRequests)).toEqual([]);
});

test('a file opens with a gutter, and a line number marks the line without repainting the page', async ({ page }) => {
  const w = watch(page);
  await page.goto('/gnohub/' + HUB_ROUTE + NET);
  await settle(page);
  await page.locator('.gh-file-name a').click();
  await settle(page);

  await expect(page).toHaveURL(/\/-\/blob\/core\.gno/);
  await expect(page.locator('.gh-blob-name')).toHaveText('core.gno');
  const lines = await page.locator('.gh-ln').count();
  expect(lines).toBeGreaterThan(1);

  // The click writes the hash with replaceState rather than letting the anchor
  // through, because a hash change fires popstate and route() would repaint the
  // whole view and lose the line the reader just asked for.
  await page.locator('.gh-ln').nth(1).click();
  await expect(page).toHaveURL(/#gh-core-gno-L2$/);
  await expect(page.locator('.gh-ln.gh-line-on')).toHaveText('2');
  await expect(page.locator('span.gh-line-on')).toHaveCount(1);
  // Still the blob, not a repainted code tab: a repaint would have thrown the
  // mark away.
  await expect(page.locator('.gh-blob-name')).toHaveText('core.gno');
  expect(w.jsErrors).toEqual([]);
  expect(unexpected(w.failedRequests)).toEqual([]);
});

test('the deploys tab lists the submission that created the package', async ({ page }) => {
  const w = watch(page);
  await page.goto('/gnohub/' + HUB_ROUTE + '/-/commits' + NET);
  await settle(page);

  const rows = page.locator('.gh-commits tbody tr');
  await expect(rows).toHaveCount(1);
  await expect(rows.first()).toContainText(HUB_CREATOR.slice(0, 8));
  await expect(rows.first().locator('.gh-dot.ok')).toBeVisible();
  // The first submission at a path is tagged as such, and a single deploy is
  // the first one.
  await expect(rows.first().locator('.gh-tag')).toHaveText('first');
  // The panel says what a "commit" here is and is not, rather than leaving the
  // github analogy to imply a diff that does not exist.
  await expect(page.locator('.gh-note')).toContainText('MsgAddPackage');
  expect(w.jsErrors).toEqual([]);
  expect(unexpected(w.failedRequests)).toEqual([]);
});

test('the dependencies tab names both directions, and a dependent links onward', async ({ page }) => {
  const w = watch(page);
  await page.goto('/gnohub/' + HUB_ROUTE + '/-/deps' + NET);
  await settle(page);

  const panels = page.locator('.gh-panel-head');
  await expect(panels.nth(0)).toContainText('imports (' + SHARED_PACKAGES + ')');
  // The account count beside the dependent count is the whole reason it is
  // shown: 60 importers is adoption if they are 60 accounts and version churn
  // if they are two, and the fixture makes them two on purpose.
  await expect(panels.nth(1)).toContainText('dependents (' + DEPENDENTS + ', from 2 accounts)');
  const first = page.locator('.gh-dep-row a').first();
  await expect(first).toBeVisible();
  await first.click();
  await settle(page);
  await expect(page).toHaveURL(/\/gnohub\/[rp]\//);
  await expect(page.locator('.gh-repo-title-path')).toBeVisible();
  expect(w.jsErrors).toEqual([]);
  expect(unexpected(w.failedRequests)).toEqual([]);
});

// r/gnoswap/v1 is nobody's deploy and everybody's breadcrumb. A 404 would be
// correct and useless; the listing is what a reader clicking a path element
// actually wanted.
test('a path that is a prefix rather than a package answers as a directory', async ({ page }) => {
  const w = watch(page);
  await page.goto('/gnohub/r/hub/nothing-here' + NET);
  await settle(page);

  await expect(page.locator('.gh-repo-title')).toContainText('directory');
  await expect(page.locator('.gh-note')).toContainText('prefix');
  expect(w.jsErrors).toEqual([]);
  // The 404 from /api/realm is the mechanism, not a bug: it is how the page
  // learns the path is not a package.
  expect(unexpected(w.failedRequests).filter(f => !/\/api\/realm\/r\/hub\/nothing-here/.test(f)))
    .toEqual([]);
});

// The rail is described twice on purpose (a Go test keeps the two identical);
// this is the half that says the entry actually navigates.
test('the rail reaches lab and gnohub', async ({ page }) => {
  const w = watch(page);
  await page.goto('/' + NET);
  await settle(page);

  await page.locator('#nav-gnohub').click();
  await settle(page);
  await expect(page).toHaveURL(/\/gnohub/);
  await expect(page.locator('#nav-gnohub')).toHaveClass(/active/);
  await expect(page.locator('#nav-lab')).toHaveClass(/in-section/);
  // A section strip, because lab has children.
  await expect(page.locator('.pagenav a', { hasText: 'experiments' })).toBeVisible();
  expect(w.jsErrors).toEqual([]);
  expect(unexpected(w.failedRequests)).toEqual([]);
});

// The forge tab is the one part of gnohub that is not a chain fact, and the
// e2e harness has no RPC at all, so what these pin is the honest-degradation
// half: a realm page must not break because a forge nobody uses is unreachable.
test('the forge tab says what it is, and degrades without breaking the page', async ({ page }) => {
  const w = watch(page);
  await page.goto('/gnohub/' + HUB_ROUTE + '/-/forge' + NET);
  await settle(page);

  // The distinction is the content of the tab, not a footnote: a reader who
  // takes "3 open issues" for a chain fact has been misled.
  await expect(page.locator('.gh-note')).toContainText('gno.land has no');
  await expect(page.locator('.gh-panel-head').first()).toContainText('forge');
  // No RPC here, so this is the unavailable path. Either way it is a rendered
  // panel and not a blank pane or a thrown error.
  await expect(page.locator('.gh-empty').first()).toBeVisible();

  expect(w.jsErrors).toEqual([]);
});

// The forge is realm state, so the read has to name a chain. It follows the
// network the realm detail was resolved on rather than the global selector:
// with all-networks picked, the detail still came from one chain, and asking
// the forge about a different one would attribute another chain's issues to
// this package.
test('the forge read follows the network the realm was resolved on', async ({ page }) => {
  const w = watch(page);
  await page.addInitScript(() => localStorage.setItem('gnoscope-network', 'all'));
  const asked = [];
  page.on('request', (req) => {
    const u = new URL(req.url());
    if (u.pathname.startsWith('/api/gnohub/forge/')) asked.push(u.searchParams.get('network'));
  });

  await page.goto('/gnohub/' + HUB_ROUTE + '/-/forge');
  await settle(page);

  expect(asked.length).toBeGreaterThan(0);
  // alpha, never "all" and never empty: the endpoint refuses both, and it is
  // right to.
  expect(asked.every((n) => n === 'alpha')).toBe(true);
  expect(w.jsErrors).toEqual([]);
});
