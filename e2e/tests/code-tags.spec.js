// Code tags: what each package's code does, as chips, each with the line that
// earned it on hover, and a filter on every page that lists packages.
//
// The fixture's realms all define Render and fresh/kit is imported by three
// of them, so the tags here are the server's own, computed from the seeded
// source by the refresh pass (-tags-interval 1s in the harness). The hostile
// test feeds the chips through a stubbed payload instead.
import { test, expect } from '@playwright/test';

import { settle, unexpected, watch } from './helpers.js';

const NET = 'network=alpha';

// The tags are computed by a pass that runs after the fixture is seeded, so a
// spec run on its own can arrive before it has. Wait for it, on a URL nothing
// else asks for, so the response cache cannot hand back an older answer.
test.beforeAll(async ({ request }) => {
  await expect.poll(async () => {
    const r = await request.get('/api/tags?' + NET + '&path=gno.land/p/fresh/kit&_=' + Date.now());
    return ((await r.json()).tags || []).length;
  }, { timeout: 15_000 }).toBeGreaterThan(0);
});

test('timeline cards carry their tags, with the evidence on hover, and a tag narrows the feed', async ({ page }) => {
  const w = watch(page);
  await page.goto('/code/timeline?' + NET + '&ns=fresh');
  await settle(page);

  const shop = page.locator('.tl-card', { has: page.locator('a.tl-path[href^="/code/r/fresh/shop"]') });
  const render = shop.locator('.tag-chip[data-tag="render"]');
  await expect(render).toHaveCount(1);
  await expect(render).toHaveAttribute('title', /^render: .+\ndefines Render \(shop\.gno:3\)$/);
  const kit = page.locator('.tl-card', { has: page.locator('a.tl-path[href^="/code/p/fresh/kit"]') });
  await expect(kit.locator('.tag-chip[data-tag="library"]')).toHaveAttribute('title', /imported by gno\.land\/r\/fresh\/.+ and 2 other packages/);
  await expect(kit.locator('.tag-chip[data-tag="render"]')).toHaveCount(0);

  // The filter row counts the chain's tags; a click narrows the feed.
  const lib = page.locator('.tl-tags .tag-chip[data-tag="library"]');
  await expect(lib).toHaveAttribute('aria-pressed', 'false');
  await lib.click();
  await expect(page).toHaveURL(/[?&]tag=library/);
  await expect(page.locator('.tl-card')).toHaveCount(1);
  await expect(page.locator('.tl-card a.tl-path')).toHaveAttribute('href', /^\/code\/p\/fresh\/kit/);
  await expect(page.locator('.tl-tags .tag-chip[data-tag="library"]')).toHaveAttribute('aria-pressed', 'true');
  // And widens it back.
  await page.locator('.tl-tags .tag-chip[data-tag="library"]').click();
  await expect(page).not.toHaveURL(/tag=/);
  await expect(page.locator('.tl-card')).toHaveCount(4);

  expect(w.jsErrors).toEqual([]);
  expect(unexpected(w.failedRequests)).toEqual([]);
});

test('a hostile tag and evidence are drawn as text', async ({ page }) => {
  const w = watch(page);
  const evil = '<img src=x onerror="window.__pwned=1">';
  // The rule table lands after the feed, which is the order that used to
  // leave a chip's tooltip without its sentence for good.
  await page.route(/\/api\/tags\?/, async (route) => {
    await new Promise((r) => setTimeout(r, 1500));
    await route.continue();
  });
  await page.route(/\/api\/code\/timeline\?/, (route) => route.fulfill({
    contentType: 'application/json',
    body: JSON.stringify({
      network: 'alpha', height: 10, total: 1,
      rows: [{
        kind: 'new', path: 'gno.land/p/evil/x', ns: 'evil', k: 'p', creator: 'g1evil00000000000000000000000000000000',
        height: 10, time: new Date().toISOString(), tx: 't1', msg: 0, files: 1, current: true,
        tags: [{ tag: evil, why: '<script>window.__pwned=1</script>' + evil }, { tag: 'token', why: evil }],
      }],
    }),
  }));
  await page.goto('/code/timeline?' + NET);
  await settle(page);
  const chips = page.locator('.tl-card .tag-chip');
  await expect(chips).toHaveCount(2);
  await expect(chips.first()).toHaveText(evil);
  await expect(chips.first()).toHaveAttribute('title', new RegExp('<script>'));
  await expect(chips.nth(1)).toHaveAttribute('title', /^token: .+\n<img/);
  expect(await page.locator('.tl-feed img:not(.tl-shot img), .tl-feed script').count()).toBe(0);
  expect(await page.evaluate(() => window.__pwned)).toBeUndefined();
  expect(w.jsErrors).toEqual([]);
  expect(unexpected(w.failedRequests)).toEqual([]);
});

test('the realms listing has a tag facet, and a realm page its chips', async ({ page }) => {
  const w = watch(page);
  await page.goto('/realms?' + NET);
  await settle(page);
  const facet = page.locator('#realms-facets .tag-chip[data-tag="render"]');
  await expect(facet).toHaveCount(1);
  const n = Number((await facet.locator('.tag-n').textContent()).replace(/,/g, ''));
  expect(n).toBeGreaterThan(3);
  await facet.click();
  await expect(page).toHaveURL(/[?&]tag=render/);
  await settle(page);
  const rows = page.locator('#realms-list tr');
  await expect(page.locator('#realms-facets .tag-chip[data-tag="render"]')).toHaveAttribute('aria-pressed', 'true');
  const shown = await rows.count();
  expect(shown).toBeGreaterThan(0);
  expect(await page.locator('#realms-list tr:has(.tag-chip[data-tag="render"])').count()).toBe(shown);

  await page.goto('/realm/r/fresh/shop?' + NET);
  await settle(page);
  const chip = page.locator('#realm-tags .tag-chip[data-tag="render"]');
  await expect(chip).toHaveAttribute('title', /defines Render \(shop\.gno:3\)/);
  await expect(chip).toHaveAttribute('href', '/realms?tag=render&network=alpha');

  expect(w.jsErrors).toEqual([]);
  expect(unexpected(w.failedRequests)).toEqual([]);
});

test('the code map: chips in the package header, a tag colour mode, and tag: in the filter', async ({ page }) => {
  const w = watch(page);
  await page.goto('/code/p/fresh/kit?' + NET);
  await settle(page);
  const chip = page.locator('.cx-pkg-head .cx-tags .tag-chip[data-tag="library"]');
  // The names come with the tree, the evidence with the request after it.
  await expect(chip).toHaveAttribute('title', /imported by gno\.land\/r\/fresh\//);

  await page.locator('.cx-filter').fill('tag:library');
  await expect(page.locator('.cx-row.cx-pkg-row, .cx-row').filter({ hasText: 'kit' }).first()).toBeVisible();
  const labels = await page.locator('.cx-tree .cx-row:not(.cx-ns) .cx-lbl').allTextContents();
  expect(labels.length).toBeGreaterThan(0);
  expect(labels.every(l => !/shop|app\b|legacy/.test(l))).toBe(true);

  await page.goto('/code?' + NET + '&color=tag');
  await settle(page);
  await expect(page.locator('.cx-seg-b[data-v="tag"]')).toHaveAttribute('aria-pressed', 'true');
  const legend = page.locator('.cx-map-legend .tag-chip[data-tag="render"]');
  await expect(legend).toHaveCount(1);
  await legend.click();
  await expect(page).toHaveURL(/[?&]tag=render/);
  await expect(page.locator('.cx-map-legend .tag-chip[data-tag="render"]')).toHaveAttribute('aria-pressed', 'true');

  expect(w.jsErrors).toEqual([]);
  expect(unexpected(w.failedRequests)).toEqual([]);
});
