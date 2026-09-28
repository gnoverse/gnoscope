// The package directory's facets: which half of it you are looking at, and
// whose packages those are.
//
// A flat list of every deployed path answers "what exists" and stops there.
import { expect, test } from '@playwright/test';

import { settle, unexpected, watch } from './helpers.js';

const FACETS = '#packages-facets';

test('the kind facet switches which half of the directory is listed', async ({ page }) => {
  const seen = watch(page);
  await page.goto('/packages');
  await settle(page);

  const kindRow = page.locator(`${FACETS} .facet-row`).first();
  await expect(kindRow).toContainText('everything');
  await expect(kindRow).toContainText('realms');
  await expect(kindRow).toContainText('pure packages');

  // The route's own default is the pure half, so that chip reads as active
  // before anything is clicked. A control with nothing selected is a control
  // that does not say what you are looking at.
  await expect(kindRow.locator('.facet.active')).toHaveText(/pure packages/);

  const pureRows = await page.locator('#packages-list tr').count();
  await kindRow.getByRole('button', { name: /everything/ }).click();
  await settle(page);
  const allRows = await page.locator('#packages-list tr').count();
  expect(allRows).toBeGreaterThan(pureRows);
  await expect(kindRow.locator('.facet.active')).toHaveText(/everything/);

  expect(seen.jsErrors).toEqual([]);
  expect(unexpected(seen.consoleErrors)).toEqual([]);
});

// A filtered view has to be a link, or a reader who filtered has to explain
// which controls to click instead of sending the URL.
test('the facets live in the URL and survive a reload', async ({ page }) => {
  await page.goto('/packages');
  await settle(page);

  await page.locator(`${FACETS} .facet-row`).first().getByRole('button', { name: /everything/ }).click();
  await settle(page);
  await expect(page).toHaveURL(/kind=all/);

  const nsRow = page.locator(`${FACETS} .facet-row`).nth(1);
  await expect(nsRow).toContainText('namespace');
  const firstNs = nsRow.locator('.facet').nth(1); // [0] is "any"
  const nsName = (await firstNs.locator('.facet-name').textContent()).trim();
  await firstNs.click();
  await settle(page);
  await expect(page).toHaveURL(new RegExp('namespace=' + nsName));

  const filtered = await page.locator('#packages-list tr').count();
  expect(filtered).toBeGreaterThan(0);

  await page.reload();
  await settle(page);
  await expect(page.locator('#packages-list tr')).toHaveCount(filtered);
  await expect(nsRow.locator('.facet.active').locator('.facet-name')).toHaveText(nsName);
});

// Picking one facet narrows the other's counts, or the control is describing a
// listing the reader is not looking at. Picking a facet must not narrow
// itself, or there is no way to see what switching would give you.
test('the counts narrow each other but not themselves', async ({ page }) => {
  await page.goto('/packages?kind=all');
  await settle(page);

  const kindRow = page.locator(`${FACETS} .facet-row`).first();
  const nsRow = page.locator(`${FACETS} .facet-row`).nth(1);
  const kindChipsBefore = await kindRow.locator('.facet').count();
  const everythingBefore = await kindRow.getByRole('button', { name: /everything/ }).textContent();

  const firstNs = nsRow.locator('.facet').nth(1);
  const nsName = (await firstNs.locator('.facet-name').textContent()).trim();
  await firstNs.click();
  await settle(page);

  // Every kind is still offered, with counts now describing the namespace.
  await expect(kindRow.locator('.facet')).toHaveCount(kindChipsBefore);
  const everythingAfter = await kindRow.getByRole('button', { name: /everything/ }).textContent();
  expect(everythingAfter).not.toBe(everythingBefore);

  // And the namespace row still lists the others, so the reader can switch.
  expect(await nsRow.locator('.facet').count()).toBeGreaterThan(2);
  await expect(nsRow.locator('.facet.active').locator('.facet-name')).toHaveText(nsName);
});

test('the symbols column ranks the pure half, which every other sort cannot', async ({ page }) => {
  await page.goto('/packages?kind=all');
  await settle(page);

  const header = page.locator('#view-packages th[data-sort="symbols"]');
  await expect(header).toBeVisible();
  await header.click();
  await settle(page);

  const counts = await page.locator('#packages-list tr td:nth-child(4)').allTextContents();
  const nums = counts.map(t => Number(t.replace(/[^0-9]/g, '')) || 0);
  expect(nums.length).toBeGreaterThan(1);
  // Descending, which is what "most symbols" means.
  for (let i = 1; i < nums.length; i++) {
    expect(nums[i]).toBeLessThanOrEqual(nums[i - 1]);
  }
  expect(nums[0]).toBeGreaterThan(0);
});

test('a package with symbols links into its docs', async ({ page }) => {
  await page.goto('/packages?kind=all&sort=symbols');
  await settle(page);
  const cell = page.locator('#packages-list tr td:nth-child(4) a').first();
  await expect(cell).toBeVisible();
  await cell.click();
  await expect(page).toHaveURL(/tab=docs/);
});

// Two things sort these headers, and they have to agree on the first click.
//
// The loader refetches the whole set ordered descending; the generic
// click-to-sort enhancer then reorders the fifty rows on screen. It used to
// start ascending, so asking a directory for "most calls" fetched exactly the
// right rows and displayed them fewest-first, which reads as the opposite of
// what the header promises. Not about the facets; it was live on every
// directory.
//
// `settle` is not enough to read the result of one of these clicks. It waits for
// `networkidle`, which is 500 ms of quiet: if the click's fetch has not been
// issued by the time Playwright looks, the network is *already* quiet and settle
// returns at once, so the read lands on the rows from before the click.
//
// That is what was failing, on main and on unrelated pull requests alike, twice
// in a run because the retry raced the same way. The values in the failure name
// it: `Expected >= 23, Received 20`, and 23 then 20 is exactly what the first
// column holds in the *descending* state this fixture produces. The read was of
// the rows the previous click left behind, not of a sort that came out wrong.
//
// Not reproduced on a developer machine, including against a deliberately slow
// /api/realms, which is why the window is one task rather than one request:
// delaying the response does not help, because networkidle waits correctly once
// a request is actually in flight.
//
// So each click waits for its own response. `sorted()` is the shape to copy for
// any other server-sorted header: start listening *before* the click, because a
// fast response can land before the waiter is attached.
test('a server-sorted column starts descending, and still toggles', async ({ page }) => {
  await page.goto('/realms');
  await settle(page);

  const header = page.locator('#view-realms th[data-sort="calls"]');
  const callsColumn = async () =>
    (await page.locator('#realms-list tr td:nth-child(4)').allTextContents())
      .map(t => Number(t.replace(/[^0-9]/g, '')) || 0);
  const sorted = async () => {
    const landed = page.waitForResponse(r => r.url().includes('/api/realms') && r.ok());
    await header.click();
    await landed;
    await settle(page);
  };

  await sorted();
  const desc = await callsColumn();
  expect(desc.length).toBeGreaterThan(2);
  for (let i = 1; i < desc.length; i++) {
    expect(desc[i], 'first click is most-called first, not fewest').toBeLessThanOrEqual(desc[i - 1]);
  }
  await expect(header).toHaveClass(/sort-desc/);

  // And the second click still flips it, which is the behaviour url-filters
  // already pins and this must not take away.
  await sorted();
  const asc = await callsColumn();
  for (let i = 1; i < asc.length; i++) {
    expect(asc[i], 'second click flips to ascending').toBeGreaterThanOrEqual(asc[i - 1]);
  }
  await expect(header).toHaveClass(/sort-asc/);
});

// A table nobody else sorts is unaffected: its first click stays ascending,
// which is what every other table on the site has always done.
test('a table the server does not order still starts ascending', async ({ page }) => {
  await page.goto('/validators');
  await settle(page);
  const th = page.locator('#view-validators table thead th').first();
  if (await th.count() === 0) test.skip();
  await expect(th).toHaveClass(/sortable/);
  expect(await th.getAttribute('data-sort')).toBeNull();
  await th.click();
  await expect(th).toHaveClass(/sort-asc/);
});

// The flake that reddened `main` on commits that could not have caused it, as a
// test that fails on demand instead of once a week.
//
// The ascending order does not come from the click. The loader's handler runs
// first, clears the tbody and paints skeleton rows, so `makeSortable`'s handler
// sorts placeholders; the real rows arrive later in the server's order, and the
// reader's direction is re-applied only by `restoreTableState`, which the
// MutationObserver schedules on a debounce.
//
// An uncapped debounce is restarted by every mutation anywhere under body, so a
// steady stream starves it and the chosen sort never lands. One hidden node
// appended every 40ms is enough, and it is nothing like a contrived load: a
// live feed, a ticking relative time or a busy CI runner all produce it. Before
// the cap this read 23, 20, 9, 7, 6, 2 after the click asking for ascending,
// which is the descending page unchanged, and that is exactly the CI failure
// (`Expected: >= 23, Received: 20`).
//
// Asserting on the order rather than on the timer: a future scheduler that does
// not debounce at all would pass this, which is correct, because the bug is the
// reader seeing the wrong order and not the mechanism that got them there.
test('a chosen sort survives a page that never stops mutating', async ({ page }) => {
  await page.goto('/realms');
  await settle(page);

  await page.evaluate(() => {
    const churn = document.createElement('div');
    churn.style.display = 'none';
    document.body.appendChild(churn);
    window.__churn = setInterval(() => {
      churn.appendChild(document.createElement('span'));
      if (churn.childNodes.length > 3) churn.removeChild(churn.firstChild);
    }, 40);
  });

  const header = page.locator('#view-realms th[data-sort="calls"]');
  const callsColumn = async () =>
    (await page.locator('#realms-list tr td:nth-child(4)').allTextContents())
      .map(t => Number(t.replace(/[^0-9]/g, '')) || 0);
  const sorted = async () => {
    const landed = page.waitForResponse(r => r.url().includes('/api/realms') && r.ok());
    await header.click();
    await landed;
    await settle(page);
  };

  try {
    await sorted();
    await sorted();

    // Polled, not read once.
    //
    // Waiting for the response and for networkidle proves the data arrived; it
    // does not prove the chosen sort has been re-applied. That happens in the
    // debounced enhance pass, which this test is deliberately starving with a
    // mutation stream, so on a loaded machine the read can land between the
    // rows arriving and the sort being restored. That is a race in the test,
    // not the regression it exists to catch: the bug it guards against leaves
    // the column descending *forever*, which a poll still fails on.
    //
    // Measured 2026-09-28: the assertion read 23, 20, ... on CI three runs in a
    // row on a branch touching nothing near this page, while the same spec
    // passed in isolation on that branch and on main.
    await expect.poll(async () => {
      const col = await callsColumn();
      if (col.length <= 2) return 'too few rows';
      for (let i = 1; i < col.length; i++) {
        if (col[i] < col[i - 1]) return 'still descending at ' + i + ': ' + col.join(',');
      }
      return 'ascending';
    }, {
      message: 'the second click still flips while the page mutates',
      timeout: 10000,
    }).toBe('ascending');
  } finally {
    // Left running, it would starve every later test in this file the same way.
    await page.evaluate(() => clearInterval(window.__churn));
  }
});
