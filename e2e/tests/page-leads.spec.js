import { test, expect } from '@playwright/test';

// Every page in the table opens with one sentence saying what it is for, once,
// under the section strip, and navigating between pages swaps it rather than
// stacking a second.
const PAGES = [
  ['/accounts', 'ranked by what they did'], ['/blocks', 'every block as it arrives'], ['/coins', 'native ugnot'],
  ['/defi', 'the money side'], ['/directory', 'who is on this chain'], ['/discover', 'what is new on this chain'],
  ['/events', 'events realms emitted'], ['/gas', 'gas used on chain'], ['/holders', 'who holds what'],
  ['/nfts', 'grc721 collections'], ['/packages', 'every package deployed'], ['/directory/people', 'find people'],
  ['/realms', 'every realm deployed'], ['/sessions', 'delegated signing keys'],
  ['/traffic', 'readers of this explorer'], ['/txs', 'newest first'], ['/validators', 'producing blocks'],
];

for (const [path, word] of PAGES) {
  test(`${path} says what it is for`, async ({ page }) => {
    const errors = [];
    page.on('pageerror', e => errors.push(String(e)));
    await page.goto(path + '?network=alpha');
    const lead = page.locator('.view.active .page-lead');
    await expect(lead).toHaveCount(1);
    await expect(lead).toContainText(word);
    expect(errors).toEqual([]);
  });
}

test('moving between pages leaves exactly one lead', async ({ page }) => {
  await page.goto('/blocks?network=alpha');
  await page.locator('#nav-txs').click();
  await expect(page.locator('.view.active .page-lead')).toHaveCount(1);
  await expect(page.locator('.view.active .page-lead')).toContainText('newest first');
});
