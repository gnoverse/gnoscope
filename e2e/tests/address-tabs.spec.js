// The address page's tab strip.
//
// The page used to be one column: the delegated-keys table, two stats bars, a
// chart, three ranked tables, the deploy list and up to 200 transaction rows,
// in that order. What these pin is the split, and specifically the three ways a
// lazy tab strip goes wrong without saying so: a pane that renders into the
// wrong container, a tab whose content is fetched before anyone asks for it,
// and a strip that offers a tab the account cannot fill.
import { expect, test } from '@playwright/test';

import { BUSY_CALLER, GRC20_FUNDER, HUB_CREATOR } from '../harness/fixture.mjs';
import { settle, unexpected, watch } from './helpers.js';

const tabs = (page) => page.locator('#address-detail-content .tabs .tab');
const pane = (page, name) => page.locator(`#address-detail-content [data-pane="${name}"]`);

test('the strip offers overview first, and every tab is reachable', async ({ page }) => {
  const seen = watch(page);
  await page.goto(`/address/${HUB_CREATOR}?network=alpha`);
  await settle(page);

  await expect(tabs(page).first()).toHaveText(/overview/);
  await expect(tabs(page).first()).toHaveClass(/active/);

  // The set an account with deploys gets. sessions is absent because this
  // account has granted none, which is the point of the next test.
  const names = await tabs(page).evaluateAll(els => els.map(e => e.getAttribute('data-tab')));
  expect(names).toEqual(['overview', 'transactions', 'defi', 'deploys', 'achievements']);

  for (const name of names) {
    await page.locator(`#address-detail-content .tab[data-tab="${name}"]`).click();
    await settle(page);
    await expect(pane(page, name), `${name} pane did not show`).toBeVisible();
    // Exactly one pane at a time. Two visible is what a strip that hides by
    // class rather than by pane looks like, and it reads as a page that simply
    // has more on it.
    await expect(page.locator('#address-detail-content [data-pane]:visible')).toHaveCount(1);
  }
  expect(unexpected(seen.consoleErrors)).toEqual([]);
});

// A tab that is always empty costs a click to find out it was empty. The count
// beside the ones that are drawn is the other half: it answers "did this
// account deploy anything" without the click at all.
test('a tab the account cannot fill is not drawn, and the drawn ones carry counts', async ({ page }) => {
  await page.goto(`/address/${BUSY_CALLER}?network=alpha`);
  await settle(page);

  // BUSY_CALLER only ever called: no deploys, no grants.
  await expect(page.locator('#address-detail-content .tab[data-tab="deploys"]')).toHaveCount(0);
  await expect(page.locator('#address-detail-content .tab[data-tab="sessions"]')).toHaveCount(0);

  const txTab = page.locator('#address-detail-content .tab[data-tab="transactions"]');
  await expect(txTab.locator('.tab-count')).toHaveText(/\(\d+\)/);

  await page.goto(`/address/${HUB_CREATOR}?network=alpha`);
  await settle(page);
  await expect(page.locator('#address-detail-content .tab[data-tab="deploys"] .tab-count')).toHaveText(/\(\d+\)/);
});

test('the open tab is in the URL, and a reload lands on it', async ({ page }) => {
  await page.goto(`/address/${HUB_CREATOR}?network=alpha`);
  await settle(page);

  await page.locator('#address-detail-content .tab[data-tab="deploys"]').click();
  await settle(page);
  await expect(page).toHaveURL(/tab=deploys/);

  await page.reload();
  await settle(page);
  await expect(page.locator('#address-detail-content .tab[data-tab="deploys"]')).toHaveClass(/active/);
  await expect(pane(page, 'deploys')).toBeVisible();

  // Back to the landing tab drops the parameter rather than spelling out the
  // default, the way every other control on this site does.
  await page.locator('#address-detail-content .tab[data-tab="overview"]').click();
  await settle(page);
  await expect(page).not.toHaveURL(/tab=/);
});

// The endpoint hands back up to 200 rows and the tab used to draw all of them.
// The pager is over that window and not over the account's whole history: a
// page number it cannot fill would be a control that does nothing.
test('the transactions tab pages the window it was given', async ({ page }) => {
  // Every network on purpose. The fixture splits this caller across two chains
  // and neither half alone is longer than one page, so a per-network view would
  // pass this test whether or not anything was paged at all.
  await page.goto(`/address/${BUSY_CALLER}?tab=transactions`);
  await settle(page);

  const api = await (await page.request.get(`/api/address/${BUSY_CALLER}`)).json();
  const loaded = (api.transactions || []).length;
  expect(loaded, 'the fixture is not longer than one page, so this test cannot see paging')
    .toBeGreaterThan(50);

  const rows = pane(page, 'transactions').locator('table tbody tr');
  await expect(rows, 'the tab drew the whole window instead of a page of it').toHaveCount(50);
  const pager = pane(page, 'transactions').locator('.pager');
  await expect(pager).toHaveCount(1);
  // Over the window, not over the account's whole history: offering a page
  // number the table cannot fill would be a control that does nothing.
  await expect(pager).toContainText('(' + loaded + ' items)');

  const before = await rows.first().textContent();
  await pager.locator('button', { hasText: 'next' }).click();
  await settle(page);
  expect(await rows.first().textContent(), 'next drew the same page again').not.toBe(before);
});

// AGENTS.md's "anything attached to a painted row has to survive that row being
// replaced", on the one control this tab added. What the reader must not get is
// a filter that silently stops applying because the rows under it were swapped.
//
// Deliberately pinned as behaviour and not as mechanism. Two things would each
// be enough on their own here (the filter persists through the URL, and the
// table element is reused so enhanceTables re-applies to the new rows), so an
// assertion aimed at either one would pass on a page where only the other still
// worked. This one fails if the filter stops filtering, whichever gave way.
test('a filter typed on one page survives turning to the next', async ({ page }) => {
  await page.goto(`/address/${BUSY_CALLER}?tab=transactions`);
  await settle(page);

  const txPane = pane(page, 'transactions');
  const rows = txPane.locator('table tbody tr');
  await expect(rows).toHaveCount(50);

  const filter = txPane.locator('.table-filter input, input.table-filter').first();
  await filter.fill('send');
  await settle(page);
  const shown = () => rows.evaluateAll(els => els.filter(e => e.style.display !== 'none').length);
  const filtered = await shown();
  expect(filtered, 'the filter matched nothing, so this test proves nothing').toBeGreaterThan(0);
  expect(filtered, 'the filter matched every row, so this test proves nothing').toBeLessThan(50);

  await txPane.locator('.pager button', { hasText: 'next' }).click();

  // Polled, not read once. Re-applying is enhanceTables' job and it runs on a
  // 100ms debounce after the mutation, so a single read right after the click
  // races it and would fail against working code.
  await expect.poll(shown, { message: 'the filter cleared when the page turned' })
    .toBeLessThan(await rows.count());
  await expect(filter, 'the box still says what it is filtering by').toHaveValue('send');
});

// Same deferral the realm page's defi tab has, and for the same reason: the
// fetch reconstructs every position from every leg the address ever had, and
// most readers of an address page never open it.
test('defi is not fetched until the tab is opened', async ({ page }) => {
  const asked = [];
  await page.route('**/api/address/*/defi*', route => { asked.push(route.request().url()); route.continue(); });
  await page.route('**/api/address/*/holdings*', route => { asked.push(route.request().url()); route.continue(); });

  await page.goto(`/address/${GRC20_FUNDER}?network=alpha`);
  await settle(page);
  expect(asked, 'defi was fetched before anyone asked for it').toEqual([]);

  await page.locator('#address-detail-content .tab[data-tab="defi"]').click();
  await settle(page);
  expect(asked.length, 'opening the tab did not fetch it').toBeGreaterThan(0);
});

// The tab was called holdings until defi replaced it, and links to it are out
// there: they land on its successor, not on the overview.
test('an old holdings link opens the defi tab', async ({ page }) => {
  await page.goto(`/address/${GRC20_FUNDER}?network=alpha&tab=holdings`);
  await settle(page);
  await expect(page.locator('#address-detail-content .tab.active')).toHaveAttribute('data-tab', 'defi');
  await expect(pane(page, 'defi')).toBeVisible();
});

// The point of the tab: positions, and one history row per transaction with
// every asset that moved in it, read from the same ledger the endpoint reports.
test('the defi tab draws positions and a per-transaction history', async ({ page }) => {
  const seen = watch(page);
  await page.goto(`/address/${GRC20_FUNDER}?network=alpha&tab=defi`);
  await settle(page);

  const api = await (await page.request.get(`/api/address/${GRC20_FUNDER}/defi?network=alpha`)).json();
  expect(api.history_total, 'the fixture funder moved nothing?').toBeGreaterThan(0);
  const defi = pane(page, 'defi');
  await expect(defi.locator('#defi-positions')).toContainText('hubcoin');
  await expect(defi.locator('#defi-history tbody tr')).toHaveCount(Math.min(api.history_total, 25));
  // Every row carries its own legs, signed.
  await expect(defi.locator('#defi-history tbody tr').first()).toContainText(/[+−]/);
  expect(unexpected(seen.consoleErrors)).toEqual([]);
});

// The history is paged by the server, one page in the DOM at a time. Stubbed:
// the fixture funder has a handful of transactions, and what is under test is
// that "older" asks the server for the next page and replaces the rows.
test('the defi history pages through the server', async ({ page }) => {
  const TOTAL = 60;
  const row = (i) => ({
    tx_hash: `tx-${i}`, block_height: 9000 - i, block_time: '2026-02-01T00:00:00Z', action: 'receive',
    legs: [{ token: 'ugnot', delta: 1000000, items: 0, symbol: 'GNOT', kind: 'native', decimals: 6, priced: false }],
    in_usd: 0, out_usd: 0,
  });
  const offsets = [];
  await page.route('**/api/address/*/defi*', route => {
    const u = new URL(route.request().url());
    const offset = Number(u.searchParams.get('offset') || 0);
    offsets.push(offset);
    const limit = Number(u.searchParams.get('limit') || 25);
    const history = [];
    for (let i = offset; i < Math.min(TOTAL, offset + limit); i++) history.push(row(i));
    route.fulfill({
      status: 200, contentType: 'application/json',
      body: JSON.stringify({
        network: 'alpha', address: GRC20_FUNDER, total_usd: 0, assets: [], balance_known: true,
        history, history_total: TOTAL, history_offset: offset, history_limit: limit, series: null,
      }),
    });
  });

  await page.goto(`/address/${GRC20_FUNDER}?network=alpha&tab=defi`);
  await settle(page);
  const rows = pane(page, 'defi').locator('#defi-history tbody tr');
  await expect(rows).toHaveCount(25);

  await pane(page, 'defi').locator('.pager button', { hasText: 'older' }).first().click();
  await settle(page);
  // Replaced, not appended: one page in the DOM, whatever page it is.
  await expect(rows).toHaveCount(25);
  expect(offsets).toContain(25);
  await expect(pane(page, 'defi').locator('.pager-info').first()).toContainText('page 2 / 3');
});

// The question #324 asked: an account page could say what it did and never what
// it holds, though the GRC20 ledger could answer it exactly. The ledgers sit
// under the defi tab now, the realm tab's own renderer pointed at an address.
test('the defi tab still reads the GRC20 ledger for a plain account', async ({ page }) => {
  const seen = watch(page);
  await page.goto(`/address/${GRC20_FUNDER}?network=alpha&tab=defi`);
  await settle(page);

  const defi = pane(page, 'defi');
  await expect(defi).toContainText('GRC20 positions');
  await expect(defi.locator('#defi-tokens tbody tr')).toHaveCount(1);
  await expect(defi.locator('#defi-tokens tbody tr').first()).toContainText('hubcoin');

  // The figure itself is checked against the ledger rather than against a
  // number typed twice: the funder was minted 1,000,000, sent 650,000 to the
  // hub over two transfers and took 150,000 back, so a page that agrees with
  // the endpoint is agreeing with that replay.
  const api = await (await page.request.get(`/api/address/${GRC20_FUNDER}/holdings?network=alpha`)).json();
  expect(api.tokens, 'the endpoint found no position to render').toHaveLength(1);
  expect(api.tokens[0].balance).toBe(1000000 + 150000 - (400000 + 250000));
  const r = await defi.locator('#defi-tokens tbody tr').first().textContent();
  expect(r.replace(/[^0-9]/g, ''), 'the drawn row does not carry the balance the endpoint reported')
    .toContain(String(api.tokens[0].balance));

  expect(unexpected(seen.consoleErrors)).toEqual([]);
});

// The renderer was written for a realm, which owns a banker whose reconstruction
// is exact. A signing account also pays gas and storage deposits, and neither
// emits a transfer event. Offering the realm's explanation here would send a
// reader hunting for transfers that are not missing.
test('an account is told why its reconstruction is short, not a realm’s reason', async ({ page }) => {
  await page.goto(`/address/${GRC20_FUNDER}?network=alpha&tab=defi`);
  await settle(page);
  await expect(pane(page, 'defi')).not.toContainText('this realm’s account');
});

// The ledger tables are one server page each, and the pager replaces the rows
// rather than appending them. It used to fetch 5,000 legs and unfold them 25 at
// a time, so a reader who kept pressing grew the page without bound.
//
// Stubbed rather than seeded. The fixture has five native legs chain-wide, and
// adding sixty more to exercise a pager would move every chain-wide coin figure
// every other spec reads.
test('the native ledger table is one server page, and pages', async ({ page }) => {
  const legs = Array.from({ length: 60 }, (_, i) => ({
    tx_hash: `leg-${i}`, account: 'account', counterparty: 'g1counterparty000000000000000000000000',
    amount: (i + 1) * 1000, coins: `${(i + 1) * 1000}ugnot`, block_height: 5000 - i,
    block_time: '2026-02-01T00:00:00Z',
  }));
  const offsets = [];
  await page.route('**/api/address/*/holdings*', route => {
    const u = new URL(route.request().url());
    const offset = Number(u.searchParams.get('flows_offset') || 0);
    const limit = Number(u.searchParams.get('flows_limit') || 50);
    offsets.push(offset);
    route.fulfill({
      status: 200, contentType: 'application/json',
      body: JSON.stringify({
        network: 'alpha', path: '', address: GRC20_FUNDER,
        balance: '9000000ugnot', balance_known: true, live_ugnot: 9000000,
        derived_ugnot: 9000000, flows_total: legs.length, flows_shown: Math.min(limit, legs.length - offset),
        flows_offset: offset, flows: legs.slice(offset, offset + limit), counterparties: [], counterparties_total: 0,
        tokens: [], token_flows: [],
      }),
    });
  });

  await page.goto(`/address/${GRC20_FUNDER}?network=alpha&tab=defi`);
  await settle(page);

  const rows = pane(page, 'defi').locator('#defi-flows tbody tr');
  await expect(rows, 'the page asked for more than one page of legs').toHaveCount(50);
  expect(offsets[0], 'the first request was not the first page').toBe(0);

  const older = pane(page, 'defi').locator('#defi-flows ~ .pager button', { hasText: 'older' });
  await older.click();
  await settle(page);
  await expect(rows).toHaveCount(10);
  expect(offsets).toContain(50);
});

// Denominated figures cannot be blended across chains, and the realm tab
// refuses that case rather than inventing a number. The account tab has to
// refuse it the same way, or the same question answers differently depending
// which page asked it.
test('defi refuses the all-networks view instead of adding two chains up', async ({ page }) => {
  await page.goto(`/address/${GRC20_FUNDER}?tab=defi`);
  await settle(page);
  await expect(pane(page, 'defi')).toContainText('denominated per chain');
});
