// What a transaction row says about the call it made.
//
// A row used to read `/r/multi/batch::Step0` — a separator this explorer
// invented for gluing two columns into one string, a syntax nobody writes, and
// nothing to tell it apart from the next four rows calling the same function.
// It also said nothing when the call moved money, so a Deposit that sent 5 GNOT
// looked exactly like a Render that sent nothing.
import { expect, test } from '@playwright/test';

import {
  MULTICALL_REALM, MULTICALL_SIGNER,
} from '../harness/fixture.mjs';
import { settle, unexpected, watch } from './helpers.js';

const txRows = (page) =>
  page.locator('#address-detail-content [data-pane="transactions"] table tbody tr');

async function openTxTab(page) {
  await page.goto(`/address/${MULTICALL_SIGNER}?network=alpha&tab=transactions`);
  await settle(page);
  await page.locator('#address-detail-content .tab[data-tab="transactions"]').click();
  await settle(page);
}

test('a call reads as gno writes it, with its arguments', async ({ page }) => {
  const seen = watch(page);
  await openTxTab(page);

  const batch = txRows(page).nth(1);
  const first = batch.locator('.msg-line').first();

  // `path.Func(...)`, never `path::func`.
  await expect(first).toContainText(`${MULTICALL_REALM.replace('gno.land/', '/')}.Step0`);
  await expect(batch).not.toContainText('::');

  // The arguments are there, bounded, and say so.
  const args = first.locator('.msg-args');
  await expect(args).toContainText('g1alice');
  await expect(args).toContainText('…');
  // The rendered cell stays inside the preview budget (96 characters) plus its
  // two parentheses, whatever the call actually passed.
  expect([...(await args.textContent())].length).toBeLessThanOrEqual(98);

  // Empty parentheses on a call with no stored arguments, not a bare name:
  // `Step0()` and `Step0` say different things and only one is a call. This is
  // also the shape of every row the backfill has not reached.
  const solo = txRows(page).first();
  await expect(solo.locator('.msg-args')).toHaveText('()');

  expect(unexpected(seen.consoleErrors)).toEqual([]);
});

test('a message that moved coins says so, and one that did not stays quiet', async ({ page }) => {
  const seen = watch(page);
  await openTxTab(page);

  const batch = txRows(page).nth(1);
  const lines = batch.locator('.msg-line');

  // Only the one message that carried coins is marked.
  await expect(batch.locator('.msg-send')).toHaveCount(1);
  // Locale-agnostic grouping, like the block-number assertion below.
  await expect(lines.first().locator('.msg-send')).toHaveText(/\+5.?000.?000 ugnot/);
  // The other three calls to the same realm carry nothing and show nothing.
  await expect(lines.nth(1).locator('.msg-send')).toHaveCount(0);

  expect(unexpected(seen.consoleErrors)).toEqual([]);
});

test('a badge and its count are one word, and the hash no longer repeats the number', async ({ page }) => {
  const seen = watch(page);
  await openTxTab(page);

  const batch = txRows(page).nth(1);

  // The count sits inside the same nowrap box as its badge, so the two cannot
  // be split across lines by a narrow column.
  const pair = batch.locator('.type-pair').first();
  await expect(pair.locator('.badge')).toHaveCount(1);
  await expect(pair).toContainText('×4');
  const box = await pair.boundingBox();
  const badge = await pair.locator('.badge').boundingBox();
  // One line: the pair is no taller than its badge plus a hair of leading.
  expect(box.height).toBeLessThan(badge.height + 8);

  // The tx cell no longer repeats what the type column already says.
  await expect(batch.locator('td').first()).not.toContainText('msgs');

  expect(unexpected(seen.consoleErrors)).toEqual([]);
});

test('the block number beside a hash is a link', async ({ page }) => {
  const seen = watch(page);
  await openTxTab(page);

  const ctx = txRows(page).first().locator('td').first().locator('.block-ctx');
  await expect(ctx).toContainText('block');
  const numberLink = ctx.locator('a');
  await expect(numberLink).toHaveCount(1);
  // Locale-agnostic: fmtNum groups digits the viewer's browser way, so the CI
  // runner's "6,002" and a French reader's "6 002" are the same assertion.
  await expect(numberLink).toHaveText(/^6.?002$/);

  await numberLink.click();
  await settle(page);
  await expect(page).toHaveURL(/\/block\/6002/);

  expect(unexpected(seen.consoleErrors)).toEqual([]);
});
