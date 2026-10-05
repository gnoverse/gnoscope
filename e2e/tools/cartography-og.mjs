// Renders the link-preview picture of every cartography view, 1200x630 JPEG,
// into pkg/web/frontend/cartography-og/, from a running gnoscope.
//
//   node tools/cartography-og.mjs [https://gnoscope.com] [mainnet]
//
// Against the live site by default, because a preview is a picture of the
// chain and the e2e fixture is not one. The pictures are committed and dated
// (cartographyPreviewDate in pkg/web/og_cartography.go): re-run this and bump
// the date when they have gone stale, nothing refreshes them on its own.
import { chromium } from '@playwright/test';
import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const base = process.argv[2] || 'https://gnoscope.com';
const network = process.argv[3] || 'mainnet';
const here = path.dirname(fileURLToPath(import.meta.url));
const out = path.resolve(here, '../../pkg/web/frontend/cartography-og');
const src = fs.readFileSync(path.resolve(here, '../../pkg/web/frontend/cartography.js'), 'utf8');
const views = [...src.matchAll(/\{ id: '([a-z]+)',/g)].map(m => m[1]);

const browser = await chromium.launch();
const page = await browser.newPage({ viewport: { width: 1200, height: 900 }, deviceScaleFactor: 1 });
for (const v of views) {
  await page.goto(`${base}/cartography?v=${v}&network=${network}`);
  await page.waitForSelector('#carto-stage svg, #carto-stage canvas', { timeout: 60_000 });
  await page.waitForTimeout(600);
  // The drawing alone, edge to edge: no site chrome, no controls, no caption.
  await page.addStyleTag({ content: `
    body > *:not(#app):not(main) { }
    header, nav, .rail, #sanity-subbar, .carto-intro, .carto-pick, .carto-blurb, .carto-bar, #carto-below, .carto-cam { display: none !important; }
    #carto-stage { position: fixed !important; inset: 0 !important; width: 1200px !important; height: 630px !important;
      border: 0 !important; border-radius: 0 !important; z-index: 9999 !important; display: flex !important;
      align-items: center; justify-content: center; }
    #carto-stage svg, #carto-stage canvas { max-height: 630px !important; width: 1200px !important; height: 630px !important; }` });
  await page.waitForTimeout(200);
  const file = path.join(out, v + '.jpg');
  await page.screenshot({ path: file, type: 'jpeg', quality: 78, clip: { x: 0, y: 0, width: 1200, height: 630 } });
  console.log(v, fs.statSync(file).size);
}
await browser.close();
