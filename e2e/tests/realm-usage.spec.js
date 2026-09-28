import { expect, test } from '@playwright/test';

import {
  USAGE_BID_CALLERS, USAGE_BID_CALLS, USAGE_CALLERS, USAGE_FAILED,
  USAGE_MESSAGES, USAGE_ROUTE, USAGE_TXS,
} from '../harness/fixture.mjs';
import { settle, unexpected, watch } from './helpers.js';

const CALLERS = Object.keys(USAGE_CALLERS);

// Reads one figure out of the summary strip by its label.
async function stat(page, label) {
  return page.locator('#tab-calls .stats-bar .stat', { hasText: label })
    .first().locator('.value').innerText();
}

const n = (v) => Number(v.replace(/[^0-9]/g, ''));

test('the calls tab counts unique callers over the whole history, not over the page', async ({ page }) => {
  const seen = watch(page);
  await page.goto(`/realm/${USAGE_ROUTE}?network=alpha&tab=calls`);
  await settle(page);

  expect(n(await stat(page, 'messages'))).toBe(USAGE_MESSAGES);
  expect(n(await stat(page, 'transactions'))).toBe(USAGE_TXS);
  // The fixture's point: 8 messages from 7 transactions from 4 addresses. A
  // count taken off the feed rows would report 8 for all three.
  expect(n(await stat(page, 'unique callers'))).toBe(CALLERS.length);
  expect(n(await stat(page, 'succeeded'))).toBe(USAGE_MESSAGES - USAGE_FAILED);
  // Bid and Claim are called; Render and Withdraw are exported and are not.
  await expect(page.locator('#tab-calls .stats-bar .stat', { hasText: 'functions used' }))
    .toContainText('of 4 exported');

  expect(seen.jsErrors).toEqual([]);
  expect(unexpected(seen.consoleErrors)).toEqual([]);
});

test('the callers table names every address with its message and transaction counts', async ({ page }) => {
  await page.goto(`/realm/${USAGE_ROUTE}?network=alpha&tab=calls`);
  await settle(page);

  const rows = page.locator('#tab-calls .usage-callers tbody tr');
  await expect(rows).toHaveCount(CALLERS.length);

  for (const [address, [messages, txs]] of Object.entries(USAGE_CALLERS)) {
    // Addresses are abbreviated in the cell; the full value is the link title.
    const row = rows.filter({ has: page.locator(`[title*="${address}"]`) }).first();
    await expect(row, `no row for ${address}`).toHaveCount(1);
    const cells = row.locator('td');
    expect(n(await cells.nth(1).innerText()), `${address} messages`).toBe(messages);
    expect(n(await cells.nth(2).innerText()), `${address} transactions`).toBe(txs);
  }
});

test('filtering by a function narrows the aggregates, not just the feed', async ({ page }) => {
  await page.goto(`/realm/${USAGE_ROUTE}?network=alpha&tab=calls`);
  await settle(page);

  const bidRow = page.locator('#tab-calls .usage-functions tbody tr', { hasText: 'Bid' }).first();
  await bidRow.locator('.usage-only').click();
  await settle(page);

  await expect(page.locator('#tab-calls .usage-chip')).toContainText('Bid');
  expect(n(await stat(page, 'messages'))).toBe(USAGE_BID_CALLS);
  // Three addresses bid; the fourth only ran a script, and a MsgRun has no
  // function at all. This is the number the old tab could not produce.
  expect(n(await stat(page, 'unique callers'))).toBe(USAGE_BID_CALLERS);

  // And clearing the chip puts everything back.
  await page.locator('#tab-calls .usage-chip-x').click();
  await settle(page);
  expect(n(await stat(page, 'messages'))).toBe(USAGE_MESSAGES);
});

test('filtering by a caller answers what that one address did here', async ({ page }) => {
  await page.goto(`/realm/${USAGE_ROUTE}?network=alpha&tab=calls`);
  await settle(page);

  const [address, [messages, txs]] = Object.entries(USAGE_CALLERS)[0];
  const row = page.locator('#tab-calls .usage-callers tbody tr')
    .filter({ has: page.locator(`[title*="${address}"]`) }).first();
  await row.locator('.usage-only').click();
  await settle(page);

  expect(n(await stat(page, 'messages'))).toBe(messages);
  expect(n(await stat(page, 'transactions'))).toBe(txs);
  expect(n(await stat(page, 'unique callers'))).toBe(1);
});

test('the failed-only filter finds the one reverted call', async ({ page }) => {
  await page.goto(`/realm/${USAGE_ROUTE}?network=alpha&tab=calls`);
  await settle(page);

  await page.locator('#tab-calls .dash-seg button:text-is("failed")').click();
  await settle(page);

  expect(n(await stat(page, 'messages'))).toBe(USAGE_FAILED);
  const feed = page.locator('#tab-calls .usage-feed tbody tr');
  await expect(feed).toHaveCount(USAGE_FAILED);
  await expect(feed.first()).toContainText('fail');
});

test('the runs filter separates MsgRun from MsgCall', async ({ page }) => {
  await page.goto(`/realm/${USAGE_ROUTE}?network=alpha&tab=calls`);
  await settle(page);

  await page.locator('#tab-calls .dash-seg button:text-is("runs")').click();
  await settle(page);

  expect(n(await stat(page, 'messages'))).toBe(1);
  await expect(page.locator('#tab-calls .usage-feed tbody tr').first()).toContainText('MsgRun');
});

// The "exported" pills read detail.calls, an int, as if it were a list, so
// calledFns was always empty and every pill rendered dimmed. The calls tab now
// upgrades them against the whole history.
test('the exported pills tell a called function from an uncalled one', async ({ page }) => {
  await page.goto(`/realm/${USAGE_ROUTE}?network=alpha`);
  await settle(page);

  // Selected on data-fn, not on the pill's text: a pill that carries a failure
  // rate reads "Bid17% fail", so a /^Bid$/ matcher finds nothing and the
  // assertion below would pass for the wrong reason on Withdraw.
  const pill = (name) => page.locator(`#tab-info .fnpill[data-fn="${name}"]`);
  await expect(pill('Bid')).not.toHaveClass(/uncalled/);
  await expect(pill('Claim')).not.toHaveClass(/uncalled/);
  await expect(pill('Withdraw')).toHaveClass(/uncalled/);
});

// ok/failed were already in the payload and rendered only inside
// the calls tab, so a function that fails half the time said nothing about it
// on the screen where somebody reads its signature and decides to call it.
//
// The fixture's Bid is called 6 times and fails once; Claim is called once and
// never fails; Withdraw is never called at all.
test('a function that fails says so on its own pill', async ({ page }) => {
  await page.goto(`/realm/${USAGE_ROUTE}?network=alpha`);
  await settle(page);

  const pill = (name) => page.locator(`#tab-info .fnpill[data-fn="${name}"]`);
  // Assert the pill itself resolves first. Without this, a selector that stops
  // matching fails the badge assertions with "element(s) not found", which
  // reads identically to the rate simply not being rendered.
  for (const name of ['Bid', 'Claim', 'Withdraw']) {
    await expect(pill(name), `${name} pill`).toHaveCount(1);
  }
  await expect(pill('Bid').locator('.fnfail')).toHaveText('17% fail');
  await expect(pill('Bid')).toHaveAttribute('title', /1 of 6 calls failed \(17%\)/);

  // No mark at all on the ones that have never failed. A row of green "0%"
  // badges is what buries the one that matters.
  await expect(pill('Claim').locator('.fnfail')).toHaveCount(0);
  await expect(pill('Claim')).toHaveAttribute('title', /none failed/);
  await expect(pill('Withdraw').locator('.fnfail')).toHaveCount(0);
});

// The headings tell the reader which control to reach for, and the control is
// the per-row "only" button, not the row. This shipped saying "click one to
// filter": the edit that was supposed to fix it matched nothing and silently
// did nothing, and no test could tell. Asserting the heading names a control
// that actually exists is what closes that.
test('each section heading names the control it points at', async ({ page }) => {
  await page.goto(`/realm/${USAGE_ROUTE}?network=alpha&tab=calls`);
  await settle(page);

  for (const section of ['usage-callers', 'usage-functions']) {
    const title = page.locator(`#tab-calls .${section} .section-title`);
    await expect(title, `${section} heading`).toContainText('only');
    await expect(title, `${section} must not point at the row`).not.toContainText('click one');
    // And the control it names is on every row.
    const rows = page.locator(`#tab-calls .${section} tbody tr`);
    expect(await rows.count()).toBeGreaterThan(0);
    await expect(rows.first().locator('.usage-only')).toHaveCount(1);
  }
});
