import { expect, test } from '@playwright/test';

import { settle } from './helpers.js';

// The rail is the one piece of chrome on every page, and it grows every time
// somebody adds a section. What it must not do as it grows is become a scroll
// container: `overflow-y: auto` on the rail itself means that the moment its
// content passes the viewport, a wheel anywhere over the left 172px scrolls the
// nav by a few pixels instead of scrolling the page.
//
// That is not hypothetical. Measured on 2026-09-23 at a 1000px viewport, the
// rail's content was exactly 1000px: it had been sitting one entry away from
// this for a while, and adding `glossary` tipped it over. The page stopped
// scrolling and a live-feed test three files away went red, which is a terrible
// way to find out.
//
// These two tests are deliberately about the behaviour rather than the CSS, so
// they survive the next refactor of how the scroll is confined.

test('a wheel over the rail chrome still scrolls the page', async ({ page }) => {
  // Short enough that the rail certainly overflows, whatever is in it today.
  await page.setViewportSize({ width: 1400, height: 600 });
  await page.goto('/blocks');
  await settle(page);

  // (0, 0) is the brand, and it is also where an automated wheel starts from.
  await page.mouse.move(5, 5);
  await page.mouse.wheel(0, 20_000);
  await page.waitForFunction(() => window.scrollY > 200);
});

test('the nav scrolls on its own, and the chrome around it stays put', async ({ page }) => {
  await page.setViewportSize({ width: 1400, height: 600 });
  await page.goto('/blocks');
  await settle(page);

  const nav = page.locator('aside nav');
  const overflows = await nav.evaluate(el => el.scrollHeight > el.clientHeight);
  expect(overflows, 'at 600px the nav has more entries than fit; if not, shorten the viewport').toBe(true);

  // The collapse button is the bottom of the rail. If the rail were the scroll
  // container it would scroll away with the links; pinned is the whole point.
  const toggle = page.locator('#rail-toggle');
  await expect(toggle).toBeInViewport();

  await nav.evaluate(el => { el.scrollTop = el.scrollHeight; });
  expect(await nav.evaluate(el => el.scrollTop)).toBeGreaterThan(0);
  await expect(toggle).toBeInViewport();
  await expect(page.locator('.rail-top')).toBeInViewport();
});

// The about block at the foot of the rail: which build is this, since when,
// what changed, and where to say it is wrong.
//
// It is one `api('version')` call away from being four empty rows, and the
// failure is silent: a reader sees `…` where a hash should be and has no
// reason to think anything went wrong. It also sits outside <nav> on purpose,
// because every <a> in there is matched against the NAV table by
// TestRailMatchesNavTable; a later edit that moves it in breaks a Go test in
// another package for reasons nobody will connect to this markup.

test('the about block names the build, the uptime and where to report a bug', async ({ page }) => {
  await page.goto('/blocks');
  await settle(page);

  // The harness builds with a plain `go build`, so the link-time values are
  // their defaults ("dev", "unknown"). Uptime is the one that is real here,
  // because it is measured by the running process rather than stamped.
  await expect(page.locator('#about-version')).not.toHaveText('…');
  await expect(page.locator('#about-version')).toHaveAttribute('href', /github\.com\/gnoverse\/gnoscope\/commit\//);
  await expect(page.locator('#about-built')).not.toHaveText('…');
  await expect(page.locator('#about-uptime')).toHaveText(/^\d+(s|m|h\s\d+m|d\s\d+h)$/);

  // The bug link's whole reason to exist is that it carries the four things a
  // report is useless without and that nobody pastes by hand.
  const body = await page.locator('#about-bug').evaluate(a => {
    a.dispatchEvent(new Event('pointerdown'));
    return decodeURIComponent(new URL(a.href).searchParams.get('body'));
  });
  expect(body).toContain('/blocks');
  expect(body).toContain('| network |');
  expect(body).toContain('| commit |');

  // Outside <nav>, where TestRailMatchesNavTable cannot see it.
  expect(await page.locator('#rail-about').evaluate(el => !!el.closest('nav'))).toBe(false);
});

test('the about block is pinned with the collapse button, and goes away with the labels', async ({ page }) => {
  await page.setViewportSize({ width: 1400, height: 600 });
  await page.goto('/blocks');
  await settle(page);

  const about = page.locator('#rail-about');
  await expect(about).toBeInViewport();
  await page.locator('aside nav').evaluate(el => { el.scrollTop = el.scrollHeight; });
  await expect(about).toBeInViewport();

  // Collapsed, the rail is 52px of icons: three keyed rows of 10px text would
  // be an unreadable smear, so the block leaves rather than being squeezed.
  await page.locator('#rail-toggle').click();
  await expect(about).toBeHidden();
  await page.locator('#rail-toggle').click();
  await expect(about).toBeVisible();
});
