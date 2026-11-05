import { expect, test } from '@playwright/test';

import { BASE_URL } from '../harness/port.mjs';
import { settle, unexpected, watch } from './helpers.js';

// /cockpit and /widget are the same payload read two ways: a page for a desk
// and a screen for a wall. What the Go tests cannot say is whether either one
// actually draws — the grading is unit-tested, the rendering is not.
//
// Note what the harness itself produces here: no RPC is configured, so every
// network's node probe fails and the verdict is legitimately not "all clear".
// That is the interesting case to render anyway. A suite that only ever saw a
// healthy instance could not tell the findings list from an empty div.

test('the cockpit draws a verdict, the findings and every section behind them', async ({ page }) => {
  const seen = watch(page);
  await page.goto('/cockpit');
  await settle(page);

  await expect(page.locator('#view-cockpit')).toHaveClass(/active/);
  await expect(page.locator('#nav-cockpit')).toHaveClass(/active/);

  const verdict = page.locator('.ckpt-verdict .ckpt-headline');
  await expect(verdict).toBeVisible();
  await expect(verdict).not.toHaveText('');

  // Every finding is named, which is the whole contract: "degraded" on its own
  // tells nobody what to do.
  const findings = page.locator('.ckpt-finding');
  expect(await findings.count()).toBeGreaterThan(0);
  await expect(findings.first().locator('.ckpt-ftitle')).not.toHaveText('');
  await expect(findings.first().locator('.ckpt-fdetail')).not.toHaveText('');

  // Both configured chains, never only the selected one.
  const chains = page.locator('#view-cockpit table tbody tr');
  await expect(chains).toHaveCount(2);
  await expect(page.locator('#view-cockpit table tbody')).toContainText('alpha');
  await expect(page.locator('#view-cockpit table tbody')).toContainText('beta');

  // text-is, not hasText: "this instance" is a substring of "what this
  // instance served" one section down.
  for (const title of ['this instance', 'chains and the index',
    'what alpha did in the last 24 hours', 'what this instance served']) {
    await expect(page.locator(`#view-cockpit .section-title:text-is("${title}")`)).toHaveCount(1);
  }

  expect(seen.jsErrors).toEqual([]);
  expect(unexpected(seen.consoleErrors)).toEqual([]);
});

// The page owns no number. Every figure belongs to another page and has to
// reach it, or the roll-up is a dead end that re-states what it cannot explain.
test('a cockpit figure leads to the page that owns it', async ({ page }) => {
  await page.goto('/cockpit');
  await settle(page);

  await page.locator('.ckpt-links a', { hasText: 'traffic' }).click();
  await settle(page);
  await expect(page).toHaveURL(/\/traffic$/);
  await expect(page.locator('#view-traffic')).toHaveClass(/active/);
});

// The widget is one screen. No rail, no header, no footer, with or without the
// ?embed=1 the rest of the app needs — it reuses that mode's class rather than
// inventing a second one, triggered by the path.
test('the widget has no chrome, and says so before the app boots', async ({ page }) => {
  await page.goto('/widget', { waitUntil: 'commit' });
  const early = await page.evaluate(() => document.documentElement.classList.contains('embed'));
  expect(early, 'the chrome must never paint, not even for a frame').toBe(true);

  const seen = watch(page);
  await settle(page);
  await expect(page.locator('header')).toBeHidden();
  await expect(page.locator('footer')).toBeHidden();
  await expect(page.locator('.rail')).toBeHidden();
  expect(seen.jsErrors).toEqual([]);
  expect(unexpected(seen.consoleErrors)).toEqual([]);
});

// What remains is the point, and a blank screen would satisfy the test above.
test('the widget shows a verdict, what is wrong by name, and five numbers', async ({ page }) => {
  await page.goto('/widget');
  await settle(page);

  await expect(page.locator('.wg-verdict')).toBeVisible();
  await expect(page.locator('.wg-verdict')).not.toHaveText('');
  // Named, not counted: the headline says how many, these say which.
  expect(await page.locator('.wg-wrong > div').count()).toBeGreaterThan(0);
  // Always five, and always the same five: a tile that comes and goes moves
  // the ones beside it, and a layout that moves is one nobody learns.
  await expect(page.locator('.wg-num')).toHaveCount(5);
  await expect(page.locator('.wg-num').first().locator('b')).not.toHaveText('');
});

// Opened on its own rather than framed, the widget keeps the app's dark
// background. Embed mode drops it so a host page can decide, and a television
// is not a host page.
test('the widget standing alone keeps its background', async ({ page }) => {
  await page.goto('/widget');
  await settle(page);
  await expect(page.locator('html')).toHaveClass(/widget-standalone/);
  const bg = await page.evaluate(() => getComputedStyle(document.body).backgroundColor);
  expect(bg).not.toBe('rgba(0, 0, 0, 0)');
});

// The header that makes it a widget rather than a page with a small
// stylesheet. A parent cannot discover whether it is allowed to frame this:
// cross-origin, a refused frame still fires `load`, so the viewer gets the
// browser's grey error page over the tile. See moul/ghoblin#63.
test('the widget says who may frame it, and nothing else does', async ({ page, request }) => {
  const widget = await request.get('/widget');
  expect(widget.headers()['content-security-policy']).toBe('frame-ancestors *');

  for (const path of ['/', '/cockpit', '/sanity']) {
    const res = await request.get(path);
    expect(res.headers()['content-security-policy'], `${path} should carry no policy`).toBeUndefined();
  }
});

// And it has to survive actually being framed, which is the one thing the
// header above exists to allow and the one thing a header test cannot prove.
test('the widget renders inside an iframe', async ({ page }) => {
  // Loaded from the origin first: setContent keeps the current document URL,
  // and on about:blank an iframe has no origin to be framed from.
  await page.goto('/');
  await page.setContent(
    `<iframe id="w" src="${BASE_URL}/widget?embed=1" style="width:900px;height:600px;border:0"></iframe>`);
  const frame = page.frameLocator('#w');
  await expect(frame.locator('.wg-verdict')).toBeVisible({ timeout: 20_000 });
  await expect(frame.locator('.wg-num')).toHaveCount(5);
  await expect(frame.locator('header')).toBeHidden();
});
