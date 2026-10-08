import { expect, test } from '@playwright/test';

import { settle, watch } from './helpers.js';

// /airdrop: an address (any prefix) against the gnoland-1 genesis sheet. The
// answers are a finding (found, not in genesis) or the absence of one (the sheet
// is not loaded, the input is not an address), and the page must never let the
// second kind read as the first.
const SOURCE = { url: 'u', commit: '30ec18996779f0185966ddb628590008999dbecf', sha256: '3379977407b57e617da5d3dcb5b8f0aeb0a739bd51e152bdc1a60fbec0215ec3', rows: 3262481 };
const SECOND = 1; const FAR = 4102444800; // 2100-01-01

const ANSWERS = {
  vesting: { input: 'cosmos1x', converted: true, status: 'found', address: 'g1pku9u3jwr8k8vjpfypqzd0uhmrwr35zk0f8u7p',
    ugnot: 332000000000000, vesting: { ugnot: 318720000000000, start: 1789225200, end: 1852383600, delayed: false }, source: SOURCE },
  plain: { input: 'g1j3', converted: false, status: 'found', address: 'g1j3et7juxr3npgdll3lml3mpv0y6m49rztjnf76',
    ugnot: 112318842569897, source: SOURCE },
  delayed: { input: 'g18c', converted: false, status: 'found', address: 'g18c0grhdx96lw2u5t9qchl390n5weu9znkwf5vm',
    ugnot: 3837075547, vesting: { ugnot: 1841860465, start: 0, end: FAR, delayed: true }, source: SOURCE },
  absent: { input: 'g1m', converted: false, status: 'not_in_genesis', address: 'g1manfred47kzduec920z88wfr64ylksmdcedlf5', source: SOURCE },
  notLoaded: { input: 'g1m', converted: false, status: 'not_loaded', address: 'g1manfred47kzduec920z88wfr64ylksmdcedlf5',
    note: 'the genesis sheet is not loaded on this instance yet, so this says nothing either way', source: SOURCE },
  invalid: { input: 'hello', converted: false, status: 'invalid', note: 'that is not a bech32 account address (cosmos1…, atone1… or g1…)', source: SOURCE },
};

async function check(page, key, typed = 'anything') {
  await page.route('**/api/airdrop*', route =>
    route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(ANSWERS[key]) }));
  await page.goto('/airdrop');
  await settle(page);
  await page.locator('#airdrop-input').fill(typed);
  await page.locator('#airdrop-go').click();
  return page.locator('#airdrop-card');
}

test('a vesting allocation says how much, when, and what is still locked', async ({ page }) => {
  const seen = watch(page);
  const card = await check(page, 'vesting', 'cosmos1x');
  await expect(card).toContainText('in genesis');
  await expect(card).toContainText('allocation: 332,000,000 GNOT');
  await expect(card).toContainText('vesting continuously from 2026-09-12 to 2028-09-12');
  await expect(card).toContainText('still locked');
  await expect(card).toContainText('re-spelled from cosmos1x');
  await expect(card).toContainText('30ec189');
  expect(seen.jsErrors).toEqual([]);
});

test('an allocation with no vesting says it was liquid', async ({ page }) => {
  const card = await check(page, 'plain');
  await expect(card).toContainText('no vesting clause');
});

test('a delayed schedule is not drawn as continuous', async ({ page }) => {
  const card = await check(page, 'delayed');
  await expect(card).toContainText('delayed schedule');
  await expect(card).not.toContainText('continuously');
});

test('not in genesis, not loaded and not an address are three different answers', async ({ page }) => {
  let card = await check(page, 'absent');
  await expect(card).toContainText('not in genesis');
  await page.unroute('**/api/airdrop*');

  card = await check(page, 'notLoaded');
  await expect(card).toContainText('sheet not loaded');
  await expect(card).not.toContainText('not in the genesis sheet');
  await page.unroute('**/api/airdrop*');

  card = await check(page, 'invalid', 'hello');
  await expect(card).toContainText('not an address');
});

test('the address in the URL is checked on load', async ({ page }) => {
  await page.route('**/api/airdrop*', route =>
    route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(ANSWERS.plain) }));
  await page.goto('/airdrop?address=g1j3et7juxr3npgdll3lml3mpv0y6m49rztjnf76');
  await settle(page);
  await expect(page.locator('#airdrop-input')).toHaveValue('g1j3et7juxr3npgdll3lml3mpv0y6m49rztjnf76');
  await expect(page.locator('#airdrop-card')).toContainText('in genesis');
});
