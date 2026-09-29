import { expect, test } from '@playwright/test';

import { settle, watch } from './helpers.js';

// The strip at the top of /blocks: one bar per block, oldest left, height from
// the transaction count and colour from the proposer.
//
// It is a picture built from the same `_blocksCache` the table below it reads,
// which is what makes it cheap and what makes it easy to break: the cache is
// repainted by an apiSWR render twice and by every live block after that, so a
// renderer that appended rather than rebuilt would grow without bound, and one
// that grouped on the raw `network` field would draw one chain as two strips
// (which it did: /api/blocks only sets that field in all-networks mode, and the
// live feed always sets it).

test('the strip draws one bar per block, linked, coloured by proposer', async ({ page }) => {
  const seen = watch(page);
  // Pinned: the harness serves two chains, and all-networks mode correctly
  // draws one strip each. The per-chain shape is what this test is about.
  await page.goto('/blocks?network=alpha');
  await settle(page);

  // `.bstrip a` and not `#blocks-strip a`: the legend under the strip carries
  // an address link per proposer, which is also an <a> inside that container.
  const bars = page.locator('#blocks-strip .bstrip a');
  // The fixture chain is 40 blocks long, proposed 0,0,1,2 repeating.
  await expect(bars).toHaveCount(40);
  await expect(page.locator('#blocks-strip-title')).toHaveText('the last 40 blocks, drawn');

  // One strip, not one per source of the same chain's rows.
  await expect(page.locator('#blocks-strip .bstrip')).toHaveCount(1);

  // Oldest left: the axis reads low to high, and the leftmost bar is the
  // oldest block rather than the newest.
  const axis = page.locator('#blocks-strip .bstrip-axis span');
  await expect(axis.first()).toHaveText('1,000');
  await expect(axis.last()).toHaveText('1,039');

  // Three proposers, and the uneven rotation is visible in the counts rather
  // than being flattened to "3 proposers".
  const legend = page.locator('#blocks-strip .bstrip-legend > span');
  await expect(legend).toHaveCount(3);
  await expect(legend.first()).toContainText('20');

  // Every bar is a way into the block it stands for. A picture that is only a
  // picture makes the reader go back to the table to act on what they saw.
  await expect(bars.first()).toHaveAttribute('title', /^1,000 · 1 tx/);
  await bars.last().click();
  await expect(page).toHaveURL(/\/block\/1039/);

  expect(seen.jsErrors).toEqual([]);
});

// Everything the fixture chain cannot show, because every one of its blocks
// carries exactly one transaction and they are all a clean minute apart.
test('an empty block is a baseline tick, a busy one is tall, and a late one is marked', async ({ page }) => {
  const t0 = Date.UTC(2026, 8, 1, 12, 0, 0);
  // 3s apart, except block 5 which arrives 30s after its predecessor.
  const gapsBefore = [0, 3, 3, 3, 3, 30, 3, 3, 3, 3];
  const txs = [0, 0, 1, 0, 0, 0, 12, 0, 0, 0];
  let at = t0;
  const rows = gapsBefore.map((gap, i) => {
    at += gap * 1000;
    return {
      hash: 'h' + i, height: 2000 + i, chain_id: 'alpha-1',
      time: new Date(at).toISOString(), num_txs: txs[i], total_txs: 100 + i,
      proposer_address_raw: 'g1val0000000000000000000000000000000',
    };
  }).reverse(); // newest first, the order /api/blocks answers in

  await page.route('**/api/blocks*', route =>
    route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(rows) }));

  await page.goto('/blocks?network=alpha');
  await settle(page);

  const bars = page.locator('#blocks-strip .bstrip a');
  await expect(bars).toHaveCount(10);

  const heights = await bars.evaluateAll(els => els.map(e => parseInt(e.style.height, 10)));
  // Empty blocks are all the baseline, and the baseline is tall enough to see:
  // at 3px the strip read as a few spikes over nothing.
  const empty = [0, 1, 3, 4, 5, 7, 8, 9].map(i => heights[i]);
  expect(new Set(empty).size).toBe(1);
  expect(empty[0]).toBeGreaterThanOrEqual(8);
  // 12 txs stands clear of 1 tx, and 1 tx stands clear of empty.
  expect(heights[6]).toBeGreaterThan(heights[2]);
  expect(heights[2]).toBeGreaterThan(empty[0]);

  // has-txs is the second channel on the same bar: opacity, not height.
  await expect(bars.nth(2)).toHaveClass(/has-txs/);
  await expect(bars.nth(0)).not.toHaveClass(/has-txs/);

  // The 30s block against a 3s median is the one thing this picture can say
  // that the table cannot, so it is the one thing marked.
  await expect(bars.nth(5)).toHaveClass(/slow/);
  await expect(bars.nth(4)).not.toHaveClass(/slow/);
  await expect(bars.nth(5)).toHaveAttribute('title', /\+30\.0s/);

  await expect(page.locator('#blocks-strip .bstrip-axis')).toContainText('median 3.0s between blocks');
  await expect(page.locator('#blocks-strip .bstrip-axis')).toContainText('peak 12 txs in a block');
});

// The strip picks its own length from the width it is given.
//
// The bars are `flex: 1 1 0` with a 2px floor, so past about 350px of content
// the flexbox stops shrinking and the row overflows to the right under
// `overflow: hidden`. What that clips is the newest blocks, which is the half
// a reader opened the page for, and it clips them silently. So the window
// shortens with the viewport instead.
test('the strip shortens rather than clipping the newest blocks', async ({ page }) => {
  const t0 = Date.UTC(2026, 8, 1, 12, 0, 0);
  const rows = Array.from({ length: 200 }, (_, i) => ({
    hash: 'h' + i, height: 3000 + i, chain_id: 'alpha-1',
    time: new Date(t0 + i * 3000).toISOString(), num_txs: i % 7 === 0 ? 2 : 0,
    total_txs: i, proposer_address_raw: 'g1val0000000000000000000000000000000',
  })).reverse();
  await page.route('**/api/blocks*', route =>
    route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(rows) }));

  for (const width of [1400, 900, 500, 375]) {
    await page.setViewportSize({ width, height: 800 });
    await page.goto('/blocks?network=alpha');
    await settle(page);

    const strip = page.locator('#blocks-strip .bstrip');
    const fit = await strip.evaluate(el => ({
      bars: el.children.length,
      clip: el.scrollWidth > el.clientWidth + 1,
    }));
    expect(fit.clip, `the strip overflows its box at ${width}px`).toBe(false);
    // Never more than the cap, and never so few that the picture stops being
    // one: 20 bars is the floor the renderer refuses to go under.
    expect(fit.bars, `bar count at ${width}px`).toBeGreaterThanOrEqual(20);
    expect(fit.bars, `bar count at ${width}px`).toBeLessThanOrEqual(120);
    // The newest block is the last bar, at every width.
    await expect(strip.locator('a').last()).toHaveAttribute('title', /^3,199 /);
  }
});
