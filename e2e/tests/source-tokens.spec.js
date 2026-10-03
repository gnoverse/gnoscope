// Source highlighting from the server's tokens.
//
// The realm source tab and the gnohub blob view paint with the regex
// highlighter first and then swap each line's children for what
// /api/source?tokens=1 says. What a Go test cannot see: that the swap
// happens, that it keeps the line ids every anchor depends on, that a
// reference reaches its declaration, and that a failing endpoint leaves the
// regex paint standing rather than an empty block.
import { test, expect } from '@playwright/test';

import { LIBRARY_ROUTE, LIBRARY_SOURCE } from '../harness/fixture.mjs';
import { settle, unexpected, watch } from './helpers.js';

const NET = 'network=alpha';
const LINES = LIBRARY_SOURCE.split('\n').length;
const lineOf = (needle) => LIBRARY_SOURCE.split('\n').findIndex(l => l.includes(needle)) + 1;

test('the source tab is drawn from tokens, keeps its line ids, and links a reference to its declaration', async ({ page }) => {
  const w = watch(page);
  await page.goto('/realm/' + LIBRARY_ROUTE + '?tab=source&' + NET);
  await settle(page);

  const pre = page.locator('#tab-source pre[data-tokens="1"]');
  await expect(pre).toHaveCount(1);
  // Every line kept its id, so ?line= and the docs tab's anchors still land.
  await expect(pre.locator(':scope > span[id^="src-toolkit-gno-L"]')).toHaveCount(LINES);
  // The bytes did not move: the tokens are a classification, not a rewrite.
  expect(await pre.textContent()).toBe(LIBRARY_SOURCE);

  await expect(pre.locator('.syn-decl[data-decl="Tree"]')).toHaveCount(1);
  await expect(pre.locator('.syn-decl[data-decl="Tree.Get"]')).toHaveCount(1);
  await expect(pre.locator('.syn-builtin', { hasText: /^false$/ }).first()).toBeVisible();
  // A whole-line comment the regex also caught, and a raw decl it never could.
  await expect(pre.locator('.syn-cmt').first()).toHaveText('// Package toolkit is the fixture\'s large package.');

  // `*Tree` in NewTree's signature is a use of the type declared above it.
  const newTreeLine = page.locator('#src-toolkit-gno-L' + lineOf('func NewTree('));
  await newTreeLine.locator('.syn-ref', { hasText: /^Tree$/ }).click();
  await expect(page.locator('#src-toolkit-gno-L' + lineOf('type Tree struct'))).toHaveClass(/syn-flash/);

  expect(w.jsErrors).toEqual([]);
  expect(unexpected(w.failedRequests)).toEqual([]);
});

test('the gnohub blob view is drawn from tokens and its gutter anchors still land', async ({ page }) => {
  const w = watch(page);
  const target = lineOf('func Walk(');
  await page.goto('/gnohub/' + LIBRARY_ROUTE + '/-/blob/toolkit.gno?' + NET + '#gh-toolkit-gno-L' + target);
  await settle(page);

  const code = page.locator('pre.gh-code[data-tokens="1"]');
  await expect(code).toHaveCount(1);
  await expect(code.locator(':scope > span[id^="gh-toolkit-gno-L"]')).toHaveCount(LINES);
  await expect(page.locator('#gh-toolkit-gno-L' + target)).toHaveClass(/gh-line-on/);

  await page.locator('#gh-toolkit-gno-L' + lineOf('func Merge(')).locator('.syn-ref', { hasText: /^Tree$/ }).first().click();
  await expect(page.locator('#gh-toolkit-gno-L' + lineOf('type Tree struct'))).toHaveClass(/gh-line-on/);
  expect(page.url()).toContain('#gh-toolkit-gno-L' + lineOf('type Tree struct'));

  expect(w.jsErrors).toEqual([]);
  expect(unexpected(w.failedRequests)).toEqual([]);
});

test('a failing tokens endpoint leaves the regex highlighting in place', async ({ page }) => {
  const w = watch(page);
  let asked = 0;
  await page.route(/\/api\/source\/.*tokens=1/, (route) => {
    asked++;
    return route.fulfill({ status: 500, contentType: 'application/json', body: '{"error":"down"}' });
  });
  await page.goto('/realm/' + LIBRARY_ROUTE + '?tab=source&' + NET);
  await settle(page);

  expect(asked).toBeGreaterThan(0);
  const pre = page.locator('#tab-source pre');
  await expect(pre).toHaveCount(1);
  await expect(pre).not.toHaveAttribute('data-tokens', /.*/);
  await expect(pre.locator(':scope > span[id^="src-toolkit-gno-L"]')).toHaveCount(LINES);
  await expect(pre.locator('.syn-kw').first()).toBeVisible();
  expect(await pre.textContent()).toBe(LIBRARY_SOURCE);

  expect(w.jsErrors).toEqual([]);
  expect(unexpected(w.failedRequests).filter(f => !/tokens=1/.test(f))).toEqual([]);
});
