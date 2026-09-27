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
  expect(names).toEqual(['overview', 'transactions', 'holdings', 'deploys', 'achievements']);

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
// fetch reconstructs a position from every transfer leg the address ever had,
// and most readers of an address page never open it.
test('holdings is not fetched until the tab is opened', async ({ page }) => {
  const asked = [];
  await page.route('**/api/address/*/holdings*', route => { asked.push(route.request().url()); route.continue(); });

  await page.goto(`/address/${GRC20_FUNDER}?network=alpha`);
  await settle(page);
  expect(asked, 'holdings was fetched before anyone asked for it').toEqual([]);

  await page.locator('#address-detail-content .tab[data-tab="holdings"]').click();
  await settle(page);
  expect(asked.length, 'opening the tab did not fetch it').toBeGreaterThan(0);
});

// The question #324 asked: an account page could say what it did and never what
// it holds, though the GRC20 ledger could answer it exactly. It is the realm
// tab's own renderer pointed at an address that has no package path.
test('the holdings tab reads the GRC20 ledger for a plain account', async ({ page }) => {
  const seen = watch(page);
  await page.goto(`/address/${GRC20_FUNDER}?network=alpha&tab=holdings`);
  await settle(page);

  const holdings = pane(page, 'holdings');
  await expect(holdings).toContainText('GRC20 positions');
  await expect(holdings.locator('#defi-tokens tbody tr')).toHaveCount(1);
  await expect(holdings.locator('#defi-tokens tbody tr').first()).toContainText('hubcoin');

  // The figure itself is checked against the ledger rather than against a
  // number typed twice: the funder was minted 1,000,000, sent 650,000 to the
  // hub over two transfers and took 150,000 back, so a page that agrees with
  // the endpoint is agreeing with that replay.
  const api = await (await page.request.get(`/api/address/${GRC20_FUNDER}/holdings?network=alpha`)).json();
  expect(api.tokens, 'the endpoint found no position to render').toHaveLength(1);
  expect(api.tokens[0].balance).toBe(1000000 + 150000 - (400000 + 250000));
  const row = await holdings.locator('#defi-tokens tbody tr').first().textContent();
  expect(row.replace(/[^0-9]/g, ''), 'the drawn row does not carry the balance the endpoint reported')
    .toContain(String(api.tokens[0].balance));

  expect(unexpected(seen.consoleErrors)).toEqual([]);
});

// The renderer was written for a realm, which owns a banker whose reconstruction
// is exact. A signing account also pays gas and storage deposits, and neither
// emits a transfer event, so its reconstruction is short by exactly that spend
// on every active account, permanently. Offering the realm's explanation here
// would send a reader hunting for transfers that are not missing.
test('an account is told why its reconstruction is short, not a realm’s reason', async ({ page }) => {
  await page.goto(`/address/${GRC20_FUNDER}?network=alpha&tab=holdings`);
  await settle(page);
  const holdings = pane(page, 'holdings');
  await expect(holdings).not.toContainText('this realm’s account');
});

// The holdings tab reuses the realm renderer, whose flow table draws 250 rows
// because on a realm page it is the only place those legs appear. An address
// page also has a transactions tab listing the same movements from the signer's
// side, so 250 here is a second copy of it: measured on mainnet the moment this
// shipped, moul's holdings tab came to 11,728px, thirteen screens, on a page
// whose whole point was to stop being eleven.
//
// Stubbed rather than seeded. The fixture has five native legs chain-wide, and
// adding sixty more to exercise a render cap would move every chain-wide coin
// figure every other spec reads.
test('the holdings flow table starts short on an address, and offers the rest', async ({ page }) => {
  const legs = Array.from({ length: 60 }, (_, i) => ({
    tx_hash: `leg-${i}`, account: 'account', counterparty: 'g1counterparty000000000000000000000000',
    amount: (i + 1) * 1000, coins: `${(i + 1) * 1000}ugnot`, block_height: 5000 + i,
    block_time: '2026-02-01T00:00:00Z',
  }));
  await page.route('**/api/address/*/holdings*', route => route.fulfill({
    status: 200, contentType: 'application/json',
    body: JSON.stringify({
      network: 'alpha', path: '', address: GRC20_FUNDER,
      balance: '9000000ugnot', balance_known: true, live_ugnot: 9000000,
      derived_ugnot: 9000000, flows_total: legs.length, flows_shown: legs.length,
      flows_offset: 0, flows: legs, counterparties: [], counterparties_total: 0,
      tokens: [], token_flows: [],
    }),
  }));

  await page.goto(`/address/${GRC20_FUNDER}?network=alpha&tab=holdings`);
  await settle(page);

  const rows = pane(page, 'holdings').locator('#defi-flows tbody tr');
  await expect(rows, 'the address tab drew the realm page’s 250-row table').toHaveCount(25);

  // Nothing is hidden, only undrawn, and the control says so and works.
  const more = pane(page, 'holdings').locator('button, a').filter({ hasText: /more/i }).first();
  await expect(more).toBeVisible();
  await more.click();
  await settle(page);
  await expect(rows).toHaveCount(50);
});

// Denominated figures cannot be blended across chains, and the realm tab
// refuses that case rather than inventing a number. The account tab has to
// refuse it the same way, or the same question answers differently depending
// which page asked it.
test('holdings refuses the all-networks view instead of adding two chains up', async ({ page }) => {
  await page.goto(`/address/${GRC20_FUNDER}?tab=holdings`);
  await settle(page);
  await expect(pane(page, 'holdings')).toContainText('denominated per chain');
});
