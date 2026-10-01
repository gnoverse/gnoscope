import { expect, test } from '@playwright/test';

import { HUB } from '../harness/fixture.mjs';
import { settle, unexpected, watch } from './helpers.js';

// The identity panel: what an address *is*, before anything about what it did.
//
// A gno address carries no type. A person's wallet, a realm's treasury, a
// realm's storage deposit and a delegated signing key are the same forty
// characters, and the page used to open on a transaction table for all four.
// For a realm that table is empty, which reads as "nothing here" when the truth
// is "this is gnoswap's pool".
//
// Derived from HUB's path by pkg/gnoaddr, which is the only way this mapping
// exists: the address is a truncated hash of the path and the chain never keeps
// the preimage. Pinned here so a change to the derivation fails a test rather
// than silently renaming every realm account on the site.
const HUB_BANKER = 'g1qql00vm7xf0mydz74md9c57tuv34znm8wm9nxu';
const HUB_DEPOSIT = 'g1pw5cc2863d22uu8l04xskj74ec2ldfq8fzuyv4';

const CARD = '#address-detail-content .card';

// Stubbing the endpoint rather than seeding four more addresses into the
// fixture: these cases are about what the panel *says*, and each verdict needs
// a chain answer the harness has no RPC to give.
function stubIdentity(page, body) {
  return page.route('**/api/address/*/identity*', route =>
    route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(body) }));
}

// End to end with nothing stubbed: the syncer wrote the package, the store
// derived its two accounts, and the endpoint resolved one back. If any link in
// that chain is broken this is the test that says so.
test('a realm account is named from the index, with no RPC and no stub', async ({ page, request }) => {
  const res = await request.get(`/api/address/${HUB_BANKER}/identity?network=alpha`);
  expect(res.status()).toBe(200);
  const body = await res.json();
  expect(body.kind).toBe('package');
  expect(body.package.path).toBe(HUB);
  expect(body.package.deposit).toBe(false);

  const deposit = await (await request.get(`/api/address/${HUB_DEPOSIT}/identity?network=alpha`)).json();
  // The two accounts must not collapse: one holds the realm's money, the other
  // the ugnot locked against its bytes, and a refund credited to the treasury
  // is the error the flag prevents.
  expect(deposit.kind).toBe('package_deposit');
  expect(deposit.package.deposit).toBe(true);
});

test('the realm account page says it is a realm and links to it', async ({ page }) => {
  const seen = watch(page);
  const response = await page.goto(`/address/${HUB_BANKER}?network=alpha`);
  expect(response.status()).toBe(200);
  await settle(page);

  const card = page.locator(CARD).first();
  await expect(card).toContainText('realm account');
  await expect(card).toContainText(HUB);
  // Not a bare assertion: the panel has to show how the mapping was obtained,
  // because it is derived and a reader has no other way to check it.
  await expect(card).toContainText('sha256');

  await card.getByText('open the realm').click();
  await expect(page).toHaveURL(/\/realm\/r\/hub\/core/);

  expect(seen.jsErrors).toEqual([]);
  expect(unexpected(seen.failedRequests)).toEqual([]);
});

// "Has anyone ever signed with this key" is the fact nothing else on the site
// can supply, and the two answers must not look alike.
test('signed and never-signed do not read the same', async ({ page }) => {
  await stubIdentity(page, {
    address: HUB_BANKER, network: 'alpha', kind: 'unsigned',
    chain: { exists: true, coins: '84984428254ugnot', has_signed: false, sequence: 0, account_number: 42 },
    transactions: 0,
  });
  await page.goto(`/address/${HUB_BANKER}?network=alpha`);
  await settle(page);

  const card = page.locator(CARD).first();
  await expect(card).toContainText('never signed');
  await expect(card).toContainText('no public key');
  // In GNOT like every other balance on the site, not raw ugnot.
  await expect(card).toContainText('84,984.42 GNOT');
});

// A vesting schedule can only be set at genesis, so it proves one. Its absence
// proves nothing, and the panel has to say that rather than printing a verdict
// it cannot support.
test('vesting proves genesis, and its absence is stated as unsettled', async ({ page }) => {
  await stubIdentity(page, {
    address: HUB_BANKER, network: 'alpha', kind: 'signer',
    chain: {
      exists: true, coins: '104763646687ugnot', has_signed: true,
      pub_key_type: '/tm.PubKeySecp256k1', sequence: 333, account_number: 3096238,
      vesting: { original: '106560000000ugnot', start_time: 1789225200, end_time: 1852383600 },
    },
    transactions: 0,
  });
  await page.goto(`/address/${HUB_BANKER}?network=alpha`);
  await settle(page);

  const card = page.locator(CARD).first();
  await expect(card).toContainText('funded when the chain started');
  await expect(card).toContainText('2028-09-12');
  await expect(card).toContainText('has signed 333 transactions');
});

test('no vesting is not reported as not in genesis', async ({ page }) => {
  await stubIdentity(page, {
    address: HUB_BANKER, network: 'alpha', kind: 'unsigned',
    chain: { exists: true, coins: '1000ugnot', has_signed: false, sequence: 0 },
    transactions: 0,
  });
  await page.goto(`/address/${HUB_BANKER}?network=alpha`);
  await settle(page);

  await expect(page.locator(CARD).first()).toContainText('does not settle it');
});

// The worst failure this panel could have: an outage rendering as a confident
// answer about the address.
test('an unreadable chain does not read as an empty account', async ({ page }) => {
  await stubIdentity(page, {
    address: HUB_BANKER, network: 'alpha', kind: 'unknown',
    chain_error: 'dial tcp: connection refused',
    transactions: 0,
  });
  await page.goto(`/address/${HUB_BANKER}?network=alpha`);
  await settle(page);

  const card = page.locator(CARD).first();
  await expect(card).toContainText('has not been established');
  await expect(card).toContainText('dial tcp');
  await expect(card).not.toContainText('no account here at all');
});

// A GRC20-only realm has no bank account at all, which is the shape that made
// the page look broken. It has to point at the tab that does hold the answer.
test('a realm with no bank account points at the defi tab', async ({ page }) => {
  await stubIdentity(page, {
    address: HUB_BANKER, network: 'alpha', kind: 'package',
    package: { address: HUB_BANKER, path: HUB, deposit: false },
    chain: { exists: false, has_signed: false, sequence: 0 },
    transactions: 0,
  });
  await page.goto(`/address/${HUB_BANKER}?network=alpha`);
  await settle(page);

  const card = page.locator(CARD).first();
  await expect(card).toContainText('no bank account at all');
  await card.getByText('the defi tab').click();
  await expect(page.locator('#address-detail-content .tab.active')).toContainText('defi');
});
