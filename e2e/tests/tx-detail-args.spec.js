// What a transaction row says about the call it made.
//
// A row used to read `/r/multi/batch::Step0` — a separator this explorer
// invented for gluing two columns into one string, a syntax nobody writes, and
// nothing to tell it apart from the next four rows calling the same function.
// It also said nothing when the call moved money, so a Deposit that sent 5 GNOT
// looked exactly like a Render that sent nothing.
import { expect, test } from '@playwright/test';

import {
  MULTICALL_ARG_ADDR, MULTICALL_REALM, MULTICALL_SIGNER,
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

  // The arguments are there, bounded, and say so. The ellipsis is on the
  // free-form argument; the address and the path beside it are kept whole and
  // shortened only for display (asserted in its own test below).
  const args = first.locator('.msg-args');
  await expect(args).toContainText('…');
  await expect(args).toContainText('some-very-long-flag-value…');
  // The rendered cell stays inside the preview budget plus its two
  // parentheses, whatever the call actually passed. Shorter than the stored
  // string, because addresses and paths are shortened for display.
  expect([...(await args.textContent())].length).toBeLessThanOrEqual(226);

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

// An argument that is an identifier is the one a reader wants to follow, and
// before this it was the one they could not: an address rendered in full pushed
// everything after it off the row, and rendering it truncated turned it into
// text nobody could click or copy.
test('an address or realm path in an argument is shortened and clickable', async ({ page }) => {
  const seen = watch(page);
  await openTxTab(page);

  const args = txRows(page).nth(1).locator('.msg-line').first().locator('.msg-args');

  // The address is a link, shown in the house short form, never in full.
  const addr = args.locator(`a[href], a`).filter({ hasText: /^g1/ }).first();
  await expect(addr).toHaveCount(1);
  const shown = await addr.textContent();
  expect(shown).toMatch(/^g1\w{6}…\w{4}$/);
  expect(shown.length).toBeLessThan(MULTICALL_ARG_ADDR.length);
  // Shortened for display only: the whole address is still there to act on.
  await expect(args).not.toContainText(MULTICALL_ARG_ADDR);

  // The realm path is a link too, in the same shortened form the rest of the
  // site uses: no `gno.land/` prefix.
  await expect(args).toContainText('/r/gnoswap/position');
  await expect(args).not.toContainText('gno.land/r/gnoswap/position');

  // Everything else stays literal, ellipsis included.
  await expect(args).toContainText('some-very-long-flag-value…');

  // Clicking the address goes to its page.
  await addr.click();
  await settle(page);
  await expect(page).toHaveURL(new RegExp(MULTICALL_ARG_ADDR));

  expect(unexpected(seen.consoleErrors)).toEqual([]);
});
