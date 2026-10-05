// Renders cover.html to ../cover.png at 2x. Needs Playwright (npx playwright
// install chromium); compress the result with pngquant before committing.
const { chromium } = require('playwright-core');
(async () => {
  const b = await chromium.launch();
  const p = await b.newPage({ deviceScaleFactor: 2, viewport: { width: 1400, height: 800 } });
  await p.goto('file://' + __dirname + '/cover.html');
  await p.evaluate(() => document.fonts.ready);
  await p.locator('#shot').screenshot({ path: __dirname + '/../cover.png' });
  await b.close();
})();
