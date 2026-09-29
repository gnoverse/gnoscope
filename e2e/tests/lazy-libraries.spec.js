import { expect, test } from '@playwright/test';

import { HUB_ROUTE } from '../harness/fixture.mjs';
import { settle, unexpected, watch } from './helpers.js';

// The chart libraries are fetched on first use (#214), not by a <script src>
// above the app script. What that buys is a first paint that does not wait on
// 676 KB from two external origins; what it costs is that `d3`, `Chart` and
// `echarts` are undefined when the app starts, and any code that reads them
// directly now reads undefined forever rather than "not yet".
//
// That is not a theoretical failure mode. It shipped: `loadDashboards` had its
// own `typeof echarts === 'undefined'` gate, which was true on every visit once
// the library stopped loading up front, so /dashboards printed "charts
// unavailable" and drew nothing. Production, until the commit this test comes
// with. pages.spec.js loads /dashboards and did not notice, because a page that
// politely says it cannot draw throws no error and logs nothing.
//
// So these assert on drawn output, not on the absence of complaint.

test('/dashboards draws its charts, having fetched ECharts on demand', async ({ page }) => {
  const seen = watch(page);

  await page.goto('/dashboards?network=alpha');
  await settle(page);

  // The message the regression printed. Named explicitly: an empty page and a
  // page saying it gave up are different bugs and should read differently.
  await expect(page.locator('#dashboards-content .dash-msg', { hasText: 'charts unavailable' }))
    .toHaveCount(0);

  // Something actually rendered. ECharts draws into a canvas or an svg
  // depending on the renderer, so either counts.
  const drawn = page.locator('#view-dashboards canvas, #view-dashboards svg');
  await expect(drawn.first()).toBeVisible({ timeout: 20_000 });

  expect(await page.evaluate(() => typeof echarts)).toBe('object');

  expect(seen.jsErrors, 'uncaught exceptions').toEqual([]);
  expect(unexpected(seen.consoleErrors), 'console errors').toEqual([]);
});

test('a page that draws no chart fetches no chart library', async ({ page }) => {
  const asked = [];
  await page.route('**cdn.jsdelivr.net/**', (route) => { asked.push(route.request().url()); route.abort(); });
  await page.route('**d3js.org/**', (route) => { asked.push(route.request().url()); route.abort(); });

  const seen = watch(page);
  await page.goto('/glossary?network=alpha');
  await settle(page);

  // The glossary is a list of definitions. It drew no chart before this change
  // either; the difference is that it no longer pays for four libraries to
  // find that out.
  await expect(page.locator('#view-glossary')).toHaveClass(/active/);
  expect(asked, 'no chart library fetched for a page with no chart').toEqual([]);

  expect(seen.jsErrors, 'uncaught exceptions').toEqual([]);
});

test('the realm page loads only what it draws with, and not echarts-gl', async ({ page }) => {
  const seen = watch(page);
  await page.goto(`/realm/${HUB_ROUTE}?network=alpha`);
  await settle(page);

  // echarts-gl is 176 KB serving exactly one chart, the caller graph on
  // /dashboards. It has no global of its own, so the tell is that nobody asked
  // for the file.
  const fetched = await page.evaluate(() => performance.getEntriesByType('resource')
    .map(r => r.name).filter(n => n.includes('echarts-gl')));
  expect(fetched, 'echarts-gl is not fetched outside the one chart that needs it').toEqual([]);

  expect(seen.jsErrors, 'uncaught exceptions').toEqual([]);
});
