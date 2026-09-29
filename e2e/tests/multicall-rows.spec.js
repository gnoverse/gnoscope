// A transaction listing draws one row per transaction, not one per message.
//
// Every row on the address page comes from a per-message table — calls,
// package_submissions, msg_runs, bank_sends — so a multicall used to arrive as
// N rows sharing one hash, one block, one timestamp and one gas figure. Drawn
// straight, the table said the account did five things a millisecond apart, the
// header counted messages and called them transactions, and the overview summed
// one gas bill five times. /txs had the opposite bug on the same data: it drew
// messages[0] and dropped the rest.
//
// What these pin is the grouping and the three numbers that move with it.
import { expect, test } from '@playwright/test';

import {
  MULTICALL_BATCH_MESSAGES, MULTICALL_CALLS_IN_BATCH, MULTICALL_GAS_USED,
  MULTICALL_MESSAGES, MULTICALL_SIGNER, MULTICALL_TXS,
} from '../harness/fixture.mjs';
import { settle, unexpected, watch } from './helpers.js';

const txPane = (page) => page.locator('#address-detail-content [data-pane="transactions"]');
const txRows = (page) => txPane(page).locator('table tbody tr');

async function openTxTab(page) {
  await page.goto(`/address/${MULTICALL_SIGNER}?network=alpha&tab=transactions`);
  await settle(page);
  await page.locator('#address-detail-content .tab[data-tab="transactions"]').click();
  await settle(page);
}

test('a multicall is one row, and its messages are listed inside it', async ({ page }) => {
  const seen = watch(page);
  await openTxTab(page);

  // Two transactions, six messages. The row count is the transaction count.
  await expect(txRows(page)).toHaveCount(MULTICALL_TXS);

  // Newest first, and the batch is the older of the two.
  const batch = txRows(page).nth(1);
  // Every message is in the DOM whether or not the fold is open: the filter box
  // and the CSV export both read the row's text, and a message they cannot see
  // is a message they cannot match or export.
  await expect(batch.locator('.msg-line')).toHaveCount(MULTICALL_BATCH_MESSAGES);
  // Three visible, the rest folded away behind a control that says how many.
  await expect(batch.locator('.msg-line:visible')).toHaveCount(3);
  await expect(batch.locator('.clamp-toggle')).toHaveText(`+${MULTICALL_BATCH_MESSAGES - 3} more`);

  await batch.locator('.clamp-toggle').click();
  await expect(batch.locator('.msg-line:visible')).toHaveCount(MULTICALL_BATCH_MESSAGES);
  await expect(batch.locator('.clamp-toggle')).toHaveText('show less');

  // The hash is drawn once, with the message count beside it.
  await expect(batch.locator('td').first()).toContainText(`×${MULTICALL_BATCH_MESSAGES} msgs`);
  // Mixed kinds, so each badge is drawn with its own count rather than one
  // badge standing in for the whole transaction.
  await expect(batch.locator('td').nth(1)).toContainText('×4');

  // The single-message transaction is untouched: no number, no indent, no fold.
  const solo = txRows(page).first();
  await expect(solo.locator('.msg-line')).toHaveCount(0);
  await expect(solo.locator('td').first()).not.toContainText('msgs');

  expect(unexpected(seen.consoleErrors)).toEqual([]);
});

test('the header counts transactions, and the gas is charged once', async ({ page }) => {
  const seen = watch(page);
  await page.goto(`/address/${MULTICALL_SIGNER}?network=alpha`);
  await settle(page);

  // The headline tile said 6 — the message count — while the identity panel
  // above it reported what the chain had signed. It says 2.
  const tile = page.locator('#address-detail-content .stats-bar .stat', { hasText: 'transactions' }).first();
  await expect(tile.locator('.value')).toHaveText(String(MULTICALL_TXS));
  // The message figure is not lost, it rides in the tooltip.
  await expect(tile).toHaveAttribute('title', new RegExp(`${MULTICALL_MESSAGES} messages`));

  // The tab strip agrees with the tile and with the rows it will draw.
  await expect(page.locator('#address-detail-content .tab[data-tab="transactions"] .tab-count'))
    .toHaveText(`(${MULTICALL_TXS})`);

  // Gas is per transaction. Summed per message it read 6 x 70000.
  const gas = page.locator('#address-detail-content .stat', { hasText: 'gas used' }).first();
  await expect(gas.locator('.value')).toHaveText(new RegExp(String(MULTICALL_GAS_USED).replace(/\B(?=(\d{3})+(?!\d))/g, '[\\s,\\u202f]?')));

  expect(unexpected(seen.consoleErrors)).toEqual([]);
});

test('/txs groups a filtered view by transaction too', async ({ page }) => {
  const seen = watch(page);
  // Filtered, because that is the view served from storage — FilteredTransactions
  // reads the same per-message tables the address page does, so before this the
  // four calls of one multicall were four rows repeating one hash.
  await page.goto('/txs?network=alpha&type=call');
  await settle(page);

  const multi = page.locator('#txs-list tr', { has: page.locator('.msg-list') }).first();
  await expect(multi).toHaveCount(1);
  await expect(multi.locator('.msg-line')).toHaveCount(MULTICALL_CALLS_IN_BATCH);
  await expect(multi.locator('td').first()).toContainText(`\u00d7${MULTICALL_CALLS_IN_BATCH} msgs`);
  // One kind, so the type column carries the count and the lines carry no badge.
  await expect(multi.locator('td').nth(3)).toContainText(`\u00d7${MULTICALL_CALLS_IN_BATCH}`);
  await expect(multi.locator('.msg-line .badge')).toHaveCount(0);

  expect(unexpected(seen.consoleErrors)).toEqual([]);
});
