// /code/timeline: what was published on one chain, newest first.
//
// What a Go test cannot see: that the feed is drawn grouped under day
// headers, that a filter is written into the URL and changes what is listed,
// that a click on the calendar narrows the feed to that day, that scrolling
// down fetches the next page by its cursor, and that a path, a name or a
// summary the chain hands us is drawn as text and never as markup.
import { test, expect } from '@playwright/test';

import { settle, unexpected, watch } from './helpers.js';

const URL0 = '/code/timeline?network=alpha';

test('the feed renders grouped by day, newest first, linking into the code map', async ({ page }) => {
  const w = watch(page);
  await page.goto(URL0);
  await settle(page);

  const days = page.locator('.tl-day');
  expect(await days.count()).toBeGreaterThanOrEqual(2);
  // The fixture's recent tail was published a few hours ago.
  await expect(days.first()).toHaveText(/^(today|yesterday)$/);
  // A header per day, never two for the same one.
  const labels = await days.allTextContents();
  expect(new Set(labels).size).toBe(labels.length);
  expect(labels.slice(1).every(l => /^[a-z]{3}, [a-z]{3} \d{1,2}, \d{4}$/i.test(l))).toBe(true);

  const first = page.locator('.tl-card').first();
  await expect(first.locator('.tl-verb').first()).toHaveText('published');
  await expect(first.locator('.tl-kind').first()).toHaveText('new');
  await expect(first.locator('a.tl-path')).toHaveAttribute('href', '/code/r/fresh/shop?network=alpha');
  await expect(first.locator('.tl-meta')).toContainText('1 file');
  await expect(page.locator('.tl-count')).toContainText('publications');

  // The rail: the code map and its timeline live under developer.
  await expect(page.locator('#nav-codetimeline')).toHaveClass(/active/);
  await expect(page.locator('#nav-developer')).toHaveClass(/in-section/);
  await expect(page.locator('#nav-code .rail-label')).toHaveText('code map');

  await first.locator('a.tl-path').click();
  await expect(page).toHaveURL(/\/code\/r\/fresh\/shop\?network=alpha/);
  await expect(page.locator('#nav-code')).toHaveClass(/active/);
  await expect(page.locator('#nav-developer')).toHaveClass(/in-section/);
  await expect(page.locator('#view-code .cx-title')).toHaveText('code map');

  expect(w.jsErrors).toEqual([]);
  expect(unexpected(w.failedRequests)).toEqual([]);
});

test('a filter is written into the URL and changes the results', async ({ page }) => {
  const w = watch(page);
  await page.goto(URL0);
  await settle(page);
  const before = await page.locator('.tl-card').count();

  await page.locator('.tl-in[aria-label="only this namespace"]').fill('fresh');
  await page.locator('.tl-in[aria-label="only this namespace"]').press('Enter');
  await expect(page).toHaveURL(/[?&]ns=fresh/);
  // FRESH_LIB, FRESH_REALM, FRESH_SHOP and FRESH_LEGACY.
  await expect(page.locator('.tl-card')).toHaveCount(4);
  expect(before).toBeGreaterThan(4);
  for (const href of await page.locator('a.tl-path').evaluateAll(as => as.map(a => a.getAttribute('href')))) {
    expect(href).toMatch(/^\/code\/[rp]\/fresh\//);
  }

  // A kind with nothing behind it: an empty feed that says so.
  await page.getByRole('button', { name: 'new version', exact: true }).click();
  await expect(page).toHaveURL(/[?&]kind=version/);
  await expect(page.locator('.tl-card')).toHaveCount(0);
  await expect(page.locator('.tl-feed')).toContainText('nothing was published');

  // A reload lands on the same view.
  await page.reload();
  await settle(page);
  await expect(page.getByRole('button', { name: 'new version', exact: true })).toHaveAttribute('aria-pressed', 'true');
  await expect(page.locator('.tl-in[aria-label="only this namespace"]')).toHaveValue('fresh');

  expect(w.jsErrors).toEqual([]);
  expect(unexpected(w.failedRequests)).toEqual([]);
});

test('a click on the calendar narrows the feed to that day', async ({ page }) => {
  const w = watch(page);
  await page.goto(URL0);
  await settle(page);

  await expect(page.locator('.tl-cal .tl-cell')).toHaveCount(53 * 7);
  // The fixture's genesis day: most of its packages land on it.
  const cell = page.locator('.tl-cell[data-day="2026-08-01"]');
  await expect(cell).toHaveAttribute('title', /^\d+ packages on Aug 1, 2026/);
  const n = Number(await cell.getAttribute('data-n'));
  expect(n).toBeGreaterThan(50);
  await expect(cell).toHaveAttribute('data-l', '4');
  await expect(page.locator('.tl-cell[data-n="0"]').first()).toHaveAttribute('data-l', '0');

  await cell.click();
  await expect(page).toHaveURL(/[?&]day=2026-08-01/);
  await expect(page.locator('.tl-cell[data-day="2026-08-01"]')).toHaveAttribute('aria-pressed', 'true');
  await expect(page.locator('.tl-day')).toHaveCount(1);
  await expect(page.locator('.tl-day')).toHaveText(/aug 1, 2026/i);
  await expect(page.locator('.tl-count')).toHaveText(n + ' publications');
  const days = await page.locator('.tl-card').evaluateAll(cs => [...new Set(cs.map(c => c.dataset.day))]);
  expect(days).toEqual(['2026-08-01']);

  // The chip undoes it.
  await page.getByRole('button', { name: /2026-08-01/ }).click();
  await expect(page).not.toHaveURL(/day=/);
  await expect(page.locator('.tl-day').first()).toHaveText(/^(today|yesterday)$/);

  expect(w.jsErrors).toEqual([]);
  expect(unexpected(w.failedRequests)).toEqual([]);
});

test('scrolling to the bottom loads the next page by its cursor', async ({ page }) => {
  const w = watch(page);
  const pages = [];
  page.on('request', (r) => { if (/\/api\/code\/timeline\?/.test(r.url())) pages.push(new URL(r.url()).searchParams.get('before')); });
  await page.goto(URL0);
  await settle(page);
  await expect(page.locator('.tl-card')).toHaveCount(50);

  await page.locator('.tl-feed > :last-child').scrollIntoViewIfNeeded();
  await expect.poll(() => page.locator('.tl-card').count()).toBeGreaterThan(50);
  await expect(page.locator('.tl-end')).toHaveText('that is everything');
  expect(pages.filter(Boolean).length).toBe(1);
  // No row twice.
  const keys = await page.locator('.tl-card a[title]').evaluateAll(as =>
    as.filter(a => a.textContent === 'tx').map(a => a.getAttribute('href')));
  expect(new Set(keys).size).toBe(keys.length);

  expect(w.jsErrors).toEqual([]);
  expect(unexpected(w.failedRequests)).toEqual([]);
});

test('a hostile path, name and summary are drawn as text', async ({ page }) => {
  const w = watch(page);
  const evil = '<img src=x onerror="window.__pwned=1">';
  await page.route(/\/api\/code\/timeline\?/, (route) => route.fulfill({
    contentType: 'application/json',
    body: JSON.stringify({
      network: 'alpha', height: 10, total: 1,
      rows: [{
        kind: 'version', path: 'gno.land/r/evil/' + evil, ns: 'evil', k: 'r',
        creator: 'g1evil00000000000000000000000000000000', user: '<b>boss</b>',
        height: 10, time: new Date().toISOString(), tx: 'tx"><script>window.__pwned=1</script>', msg: 0,
        files: 1, current: true, lines: 3, summary: '<script>window.__pwned=1</script>' + evil,
        family: 'gno.land/r/evil/<i>fam</i>', prev: 'gno.land/r/evil/<u>old</u>', nth: 1,
      }],
    }),
  }));
  await page.goto(URL0);
  await settle(page);

  const card = page.locator('.tl-card');
  await expect(card).toHaveCount(1);
  // Short, and still text: the namespace is a name, so it leads.
  await expect(card.locator('.tl-path')).toHaveText('evil/' + evil);
  await expect(card.locator('.tl-path')).toHaveAttribute('title', new RegExp('^r/evil/'));
  await expect(card.locator('.tl-who')).toHaveText('@<b>boss</b>');
  await expect(card.locator('.tl-sum')).toHaveText('<script>window.__pwned=1</script>' + evil);
  await expect(card.locator('.tl-gen')).toHaveAttribute('title', 'a new version of r/evil/<i>fam</i>, after r/evil/<u>old</u>');
  // The one image allowed is the realm's own picture, asked for by URL.
  expect(await page.locator('.tl-feed img:not(.tl-shot img), .tl-feed script, .tl-feed b, .tl-feed i, .tl-feed u').count()).toBe(0);
  expect(await page.evaluate(() => window.__pwned)).toBeUndefined();

  expect(w.jsErrors).toEqual([]);
  expect(unexpected(w.failedRequests)).toEqual([]);
});

// A card is read at a glance: who, by the name they registered when there is
// one; what, by the package's own name and version, the namespace only when
// it is a name. The rest is on hover and behind the copy buttons.
test('names are short: @name, a short address, the package name and its version', async ({ page }) => {
  const w = watch(page);
  const now = new Date().toISOString();
  const addr = 'g1leu8d2vsplhehcfkjg50mwgdpxdkt8tztu95wr';
  const row = (o) => ({ k: 'r', height: 10, time: now, msg: 0, files: 1, current: false, nth: 1, ...o });
  await page.route(/\/api\/code\/timeline\?/, (route) => route.fulfill({
    contentType: 'application/json',
    body: JSON.stringify({
      network: 'alpha', height: 10, total: 7,
      rows: [
        row({ kind: 'redeploy', path: 'gno.land/r/moul/relay/v0', ns: 'moul', creator: 'g1moul0000000000000000000000000000000000', user: 'moul', tx: 't1', nth: 3 }),
        row({ kind: 'version', path: 'gno.land/r/' + addr + '/bubblerumble6', ns: addr, creator: addr, tx: 't2',
          family: 'gno.land/r/' + addr + '/bubblerumble', prev: 'gno.land/r/' + addr + '/bubblerumble5', gen: 6 }),
        row({ kind: 'new', path: 'gno.land/p/' + addr + '/relay/v0', ns: addr, k: 'p', creator: addr, tx: 't3' }),
        row({ kind: 'new', path: 'gno.land/r/moul/x/daily/governor/v1', ns: 'moul', creator: addr, tx: 't4' }),
        row({ kind: 'new', path: 'gno.land/r/gnoswap/pool', ns: 'gnoswap', creator: addr, tx: 't5' }),
        row({ kind: 'new', path: 'gno.land/r/' + addr + '/gnofly/nfts/market/bazaar/v0/gnofly', ns: addr, creator: addr, tx: 't6' }),
        row({ kind: 'new', path: 'gno.land/r/' + addr + '/averyveryverylongapp/with/many/nested/segments/inside/pkg/v2', ns: addr, creator: addr, tx: 't7' }),
      ],
    }),
  }));
  await page.goto(URL0);
  await settle(page);

  const cards = page.locator('.tl-card');
  await expect(cards).toHaveCount(7);
  const path = (i) => cards.nth(i).locator('.tl-path');
  const who = (i) => cards.nth(i).locator('.tl-who');

  // Registered: @name. The namespace is a name, so it leads the path.
  await expect(who(0)).toHaveText('@moul');
  await expect(path(0)).toHaveText('moul/relay/v0');
  await expect(path(0)).toHaveAttribute('title', /^r\/moul\/relay\/v0\n/);

  // Not registered: a short address, the whole one on hover and on copy.
  await expect(who(1)).toHaveText('g1leu8d2\u202695wr');
  await expect(who(1)).toHaveAttribute('title', addr);
  await expect(cards.nth(1).locator('.tl-whobox .copy-btn')).toHaveAttribute('aria-label', 'copy the address');
  // An address namespace says nothing, so it is dropped; the version is said
  // in words.
  await expect(path(1)).toHaveText('bubblerumble6');
  await expect(cards.nth(1).locator('.tl-gen')).toHaveText('(6th version)');
  await expect(cards.nth(1).locator('.tl-gen')).toHaveAttribute('title', /bubblerumble, after r\/g1leu.*\/bubblerumble5$/);
  // A bare version segment keeps its parent.
  await expect(path(2)).toHaveText('relay/v0');
  // A short sub-path stays whole: it is what tells siblings apart.
  await expect(path(3)).toHaveText('moul/x/daily/governor/v1');
  await expect(path(4)).toHaveText('gnoswap/pool');
  await expect(path(5)).toHaveText('gnofly/nfts/market/bazaar/v0/gnofly');
  // A long one is cut in the middle: the app, the cut, the package.
  await expect(path(6)).toHaveText('averyveryverylongapp/\u2026/pkg/v2');
  for (let i = 0; i < 7; i++) await expect(path(i)).not.toContainText('g1');

  // Copy puts the whole path on the clipboard, not the short one.
  await page.context().grantPermissions(['clipboard-read', 'clipboard-write']);
  await cards.nth(1).hover();
  await cards.nth(1).locator('.tl-pathbox .copy-btn').click();
  expect(await page.evaluate(() => navigator.clipboard.readText())).toBe('r/' + addr + '/bubblerumble6');

  expect(w.jsErrors).toEqual([]);
  expect(unexpected(w.failedRequests)).toEqual([]);
});

// A realm card carries what the realm looks like; a pure package has no page
// to show and carries nothing. The box is sized before the image arrives, so
// a feed of fifty cards does not jump as fifty pictures land.
test('realm cards carry a sized, lazy thumbnail, pure ones none, and nothing moves when it lands', async ({ page }) => {
  const w = watch(page);
  let release;
  const gate = new Promise(r => { release = r; });
  await page.route(/\/api\/shot\?/, async (route) => { await gate; await route.continue(); });
  await page.goto(URL0 + '&ns=fresh');
  await expect(page.locator('.tl-card')).toHaveCount(4);

  const realm = page.locator('.tl-card', { has: page.locator('a.tl-path[href^="/code/r/fresh/shop"]') });
  const pure = page.locator('.tl-card', { has: page.locator('a.tl-path[href^="/code/p/fresh/kit"]') });
  const img = realm.locator('.tl-shot img');
  await expect(img).toHaveCount(1);
  await expect(img).toHaveAttribute('loading', 'lazy');
  await expect(img).toHaveAttribute('src', /\/api\/shot\?.*size=thumb/);
  await expect(img).toHaveAttribute('width', /\d+/);
  await expect(img).toHaveAttribute('height', /\d+/);
  expect(await img.getAttribute('onerror')).toBeNull();
  await expect(pure.locator('.tl-shot, img')).toHaveCount(0);

  // Every card's box with the pictures still in flight, then after they land.
  const boxes = () => page.locator('.tl-card').evaluateAll(cs => cs.map(c => {
    const r = c.getBoundingClientRect();
    return [Math.round(r.top), Math.round(r.height)];
  }));
  const before = await boxes();
  const shotBox = await realm.locator('.tl-shot').boundingBox();
  expect(Math.round(shotBox.width)).toBe(128);
  expect(Math.round(shotBox.height)).toBe(72);
  release();
  await expect.poll(() => img.evaluate(i => i.complete && i.naturalWidth > 0)).toBe(true);
  await expect(realm.locator('.tl-shot')).not.toHaveClass(/loading/);
  expect(await boxes()).toEqual(before);

  expect(w.jsErrors).toEqual([]);
  expect(unexpected(w.failedRequests)).toEqual([]);
});
