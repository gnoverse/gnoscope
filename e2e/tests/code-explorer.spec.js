// /code: the code explorer.
//
// What a Go test cannot see: that the tree is drawn from /api/code/tree and
// narrows as you type, that the palette finds a file and opens it, that a #L
// link lands on its line, that the map zooms on a click, that switching chain
// asks the other chain, that a file opened twice is fetched once, and that a
// path or a file name the chain hands us is drawn as text and never as markup.
import { test, expect } from '@playwright/test';

import { LIBRARY_ROUTE, LIBRARY_SOURCE } from '../harness/fixture.mjs';
import { settle, unexpected, watch } from './helpers.js';

const lineOf = (needle) => LIBRARY_SOURCE.split('\n').findIndex(l => l.includes(needle)) + 1;
const FILE_URL = '/code/' + LIBRARY_ROUTE + '/-/toolkit.gno?network=alpha';

test('the tree renders, narrows as you type, and opens a file from the keyboard', async ({ page }) => {
  const w = watch(page);
  await page.goto('/code?network=alpha');
  await settle(page);

  const rows = page.locator('.cx-tree .cx-row');
  await expect(rows.first()).toBeVisible();
  const before = await rows.count();
  expect(before).toBeGreaterThan(5);
  await expect(page.locator('.cx-stats')).toContainText('packages');

  await page.locator('.cx-filter').fill('toolkit');
  await expect(page.locator('.cx-tree-foot')).toContainText('1 of');
  await expect(page.locator('.cx-row.cx-pkg .cx-lbl')).toHaveText(['toolkit']);
  await expect(page.locator('.cx-row.cx-file .cx-lbl')).toHaveText(['toolkit.gno']);
  expect(await rows.count()).toBeLessThan(before);

  // Down from the filter lands in the tree; down again reaches the file.
  await page.locator('.cx-filter').press('ArrowDown');
  await expect(page.locator('.cx-tree')).toBeFocused();
  await page.keyboard.press('j');
  await page.keyboard.press('j');
  await page.keyboard.press('Enter');
  await expect(page).toHaveURL(/\/code\/p\/hub\/toolkit\/-\/toolkit\.gno\?network=alpha/);
  await expect(page.locator('pre.cx-code[data-tokens="1"]')).toHaveCount(1);
  expect(await page.locator('pre.cx-code').textContent()).toBe(LIBRARY_SOURCE);

  expect(w.jsErrors).toEqual([]);
  expect(unexpected(w.failedRequests)).toEqual([]);
});

test('ctrl-k jumps to a file by a fuzzy query', async ({ page }) => {
  const w = watch(page);
  await page.goto('/code?network=alpha');
  await settle(page);

  await page.keyboard.press('Control+k');
  const input = page.locator('.cx-pal-input');
  await expect(input).toBeFocused();
  // A subsequence, not a substring: hub/toolkit's main file.
  await input.fill('htktgno');
  const first = page.locator('.cx-pal-opt').first();
  await expect(first).toContainText('p/hub/toolkit/toolkit.gno');
  // The matched letters are marked, as text.
  await expect(first.locator('mark').first()).toBeVisible();
  await input.press('Enter');

  await expect(page.locator('#cx-palette')).toHaveCount(0);
  await expect(page).toHaveURL(/\/code\/p\/hub\/toolkit\/-\/toolkit\.gno/);
  await expect(page.locator('.cx-blob-name')).toHaveText('toolkit.gno');

  // "/" opens it too, here and only here, instead of the site search.
  await page.locator('.cx-main').click({ position: { x: 5, y: 5 } });
  await page.keyboard.press('/');
  await expect(page.locator('.cx-pal-input')).toBeFocused();
  await page.keyboard.press('Escape');
  await expect(page.locator('#cx-palette')).toHaveCount(0);

  expect(w.jsErrors).toEqual([]);
  expect(unexpected(w.failedRequests)).toEqual([]);
});

test('a #L deep link marks and scrolls to its line, and a reference jumps to its declaration', async ({ page }) => {
  const w = watch(page);
  const target = lineOf('func Merge(');
  await page.setViewportSize({ width: 1400, height: 500 });
  await page.goto(FILE_URL + '#L' + target);
  await settle(page);

  const line = page.locator('#cx-L' + target);
  await expect(line).toHaveClass(/cx-line-on/);
  await expect(line).toBeInViewport();
  await expect(page.locator('.cx-gutter .cx-ln.cx-line-on')).toHaveText(String(target));

  // `*Tree` in Merge's signature resolves to the type, in the same file.
  await page.locator('pre.cx-code[data-tokens="1"]').waitFor();
  await line.locator('.syn-ref', { hasText: /^Tree$/ }).first().click();
  const decl = lineOf('type Tree struct');
  await expect(page.locator('#cx-L' + decl)).toHaveClass(/cx-line-on/);
  expect(page.url()).toMatch(new RegExp('#L' + decl + '$'));

  expect(w.jsErrors).toEqual([]);
  expect(unexpected(w.failedRequests)).toEqual([]);
});

test('a file opened twice is fetched once, and the second open is drawn warm', async ({ page }) => {
  const w = watch(page);
  const bodies = [];
  page.on('request', (r) => {
    const u = r.url();
    if (/\/api\/source\/p\/hub\/toolkit\?.*at=/.test(u) && !/tokens=1/.test(u)) bodies.push(u);
  });
  await page.goto('/code/' + LIBRARY_ROUTE + '?network=alpha');
  await settle(page);
  await expect(page.locator('.cx-blob-name')).toHaveText('toolkit.gno');
  expect(bodies.length).toBe(1);

  // Away to another package inside the app, and back.
  await page.locator('.cx-filter').fill('util00');
  await page.locator('.cx-row.cx-pkg').first().click();
  await expect(page.locator('.cx-blob-name')).toHaveText('util00.gno');
  await page.goBack();
  await expect(page.locator('.cx-blob-name')).toHaveText('toolkit.gno');
  await expect(page.locator('.cx-viewer')).toHaveAttribute('data-warm', '1');
  expect(bodies.length).toBe(1);

  expect(w.jsErrors).toEqual([]);
  expect(unexpected(w.failedRequests)).toEqual([]);
});

test('the map zooms into a namespace, then a package, and a file click opens it', async ({ page }) => {
  const w = watch(page);
  await page.goto('/code?network=alpha');
  await settle(page);

  const map = page.locator('.cx-map');
  await expect(map).toHaveAttribute('data-level', 'chain');
  expect(Number(await map.getAttribute('data-leaves'))).toBeGreaterThan(10);
  const canvas = page.locator('.cx-map-canvas');
  const box = await canvas.boundingBox();
  const at = { x: Math.round(box.width / 2), y: Math.round(box.height / 2) };

  await canvas.hover({ position: at });
  await expect(page.locator('.cx-map-tip')).toBeVisible();

  await canvas.click({ position: at });
  await expect(map).toHaveAttribute('data-level', 'ns');
  await expect(page).toHaveURL(/[?&]zoom=[^&/]+(&|$)/);
  await canvas.click({ position: at });
  await expect(map).toHaveAttribute('data-level', 'pkg');
  await expect(page).toHaveURL(/[?&]zoom=gno\.land%2F/);
  // Back zooms out, because a zoom is a history entry.
  await page.goBack();
  await expect(map).toHaveAttribute('data-level', 'ns');
  await canvas.click({ position: at });
  await expect(map).toHaveAttribute('data-level', 'pkg');
  await canvas.click({ position: at });
  await expect(page).toHaveURL(/\/code\/.+\/-\/.+\?network=alpha/);
  await expect(page.locator('pre.cx-code')).toHaveCount(1);

  expect(w.jsErrors).toEqual([]);
  expect(unexpected(w.failedRequests)).toEqual([]);
});

test('switching chain refetches the tree for the other chain', async ({ page }) => {
  const w = watch(page);
  const trees = [];
  page.on('request', (r) => { if (r.url().includes('/api/code/tree')) trees.push(new URL(r.url()).searchParams.get('network')); });
  await page.goto('/code?network=alpha');
  await settle(page);
  const alphaCount = await page.locator('.cx-stats b').first().textContent();

  await page.locator('.cx-net', { hasText: 'beta' }).click();
  await expect(page).toHaveURL(/network=beta/);
  await expect(page.locator('.cx-net[aria-pressed="true"]')).toHaveText('beta');
  await expect(page.locator('.cx-stats b').first()).toHaveText('2');
  expect(alphaCount).not.toBe('2');
  expect(trees).toContain('alpha');
  expect(trees).toContain('beta');
  await expect(page.locator('.cx-row.cx-ns .cx-lbl', { hasText: /^beta$/ })).toHaveCount(1);

  expect(w.jsErrors).toEqual([]);
  // The header relinks its live feed on a chain switch, which aborts the old
  // stream: the site's own behaviour, not this page's.
  expect(unexpected(w.failedRequests).filter(f => !/\/api\/live\?/.test(f))).toEqual([]);
});

test('all-networks mode picks one chain and says which', async ({ page }) => {
  const w = watch(page);
  await page.goto('/code?network=all');
  await settle(page);
  await expect(page).toHaveURL(/network=(alpha|beta)/);
  await expect(page.locator('.cx-net[aria-pressed="true"]')).toHaveCount(1);
  await expect(page.locator('.cx-map')).toHaveCount(1);
  expect(w.jsErrors).toEqual([]);
  expect(unexpected(w.failedRequests)).toEqual([]);
});

test('hostile paths and file names are drawn as text', async ({ page }) => {
  const w = watch(page);
  const evilPkg = 'gno.land/r/evil/<img src=x onerror="window.__pwned=1">';
  const evilFile = '<script>window.__pwned=2</script>.gno';
  const tree = {
    network: 'alpha', height: 9, count: 1, files: 1, lines: 3, bytes: 40, since: '', window_days: 30,
    packages: [{ p: evilPkg, ns: 'evil"><b>x</b>', k: 'r', h: 9, f: [[evilFile, 3, 40]], l: 3, b: 40, c: 2 }],
  };
  await page.route(/\/api\/code\/tree/, route => route.fulfill({ contentType: 'application/json', body: JSON.stringify(tree) }));
  const body = 'package evil\n\n// <img src=x onerror="window.__pwned=3">\n';
  await page.route(/\/api\/source\//, (route) => {
    const u = new URL(route.request().url());
    if (u.searchParams.get('tokens')) return route.fulfill({ status: 500, contentType: 'application/json', body: '{"error":"no"}' });
    if (u.searchParams.get('at')) {
      return route.fulfill({ contentType: 'application/json', body: JSON.stringify({ path: evilPkg, network: 'alpha', files: [{ name: evilFile, body }] }) });
    }
    return route.fulfill({ contentType: 'application/json', body: JSON.stringify({ path: evilPkg, network: 'alpha', stamp: { height: 9 }, files: [], siblings: [], submissions: 1 }) });
  });

  await page.goto('/code?network=alpha');
  await settle(page);
  await expect(page.locator('.cx-row.cx-ns .cx-lbl')).toHaveText('evil"><b>x</b>');
  await page.locator('.cx-row.cx-ns').click();
  await expect(page.locator('.cx-row.cx-pkg .cx-lbl')).toHaveText('<img src=x onerror="window.__pwned=1">');
  await page.locator('.cx-row.cx-pkg').click();
  await expect(page.locator('.cx-blob-name')).toHaveText(evilFile);
  await expect(page.locator('.cx-pkg-path')).toHaveText(evilPkg);
  await expect(page.locator('pre.cx-code')).toContainText('onerror="window.__pwned=3"');

  await page.keyboard.press('Control+k');
  await page.locator('.cx-pal-input').fill('script');
  await expect(page.locator('.cx-pal-opt').first()).toContainText('<script>');

  expect(await page.evaluate(() => window.__pwned)).toBeUndefined();
  expect(await page.locator('#view-code img, #view-code script, #view-code b:not(.cx-stats b)').count()).toBe(0);
  expect(await page.locator('#cx-palette img, #cx-palette script').count()).toBe(0);
  expect(w.jsErrors).toEqual([]);
  expect(unexpected(w.failedRequests).filter(f => !/tokens=1/.test(f))).toEqual([]);
});
