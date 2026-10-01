// Render profile-banner.html → scarlixos-banner.png at 1920x480 (GitHub README banner)
const { chromium } = require('playwright');
const path = require('path');

(async () => {
  const browser = await chromium.launch();
  const ctx = await browser.newContext({ viewport: { width: 1920, height: 480 }, deviceScaleFactor: 2 });
  const page = await ctx.newPage();
  const fileUrl = 'file://' + path.resolve(__dirname, 'docs/profile-banner.html');
  await page.goto(fileUrl, { waitUntil: 'networkidle' });
  await page.waitForTimeout(500); // let fonts/gradients settle
  await page.screenshot({
    path: path.join(__dirname, 'docs/scarlixos-banner.png'),
    clip: { x: 0, y: 0, width: 1920, height: 480 },
    omitBackground: false,
  });
  await browser.close();
  console.log('banner written: docs/scarlixos-banner.png');
})();
