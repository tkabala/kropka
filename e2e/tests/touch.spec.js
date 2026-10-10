// Real touch events, sent over CDP, so the page's own gesture code runs.
import { expect, test } from "../fixtures.js";

async function swipe(page, [x0, y0], [x1, y1], ms = 150) {
  const cdp = await page.context().newCDPSession(page);
  const touch = (type, pts) => cdp.send("Input.dispatchTouchEvent", { type, touchPoints: pts.map(([x, y]) => ({ x, y })) });
  await touch("touchStart", [[x0, y0]]);
  const steps = 8;
  for (let i = 1; i <= steps; i++) {
    await touch("touchMove", [[x0 + ((x1 - x0) * i) / steps, y0 + ((y1 - y0) * i) / steps]]);
    await page.waitForTimeout(ms / steps);
  }
  await touch("touchEnd", []);
  await cdp.detach();
}

test("swipes step through the viewer and close it", async ({ page, gallery }) => {
  await gallery.open();
  const { width: w, height: h } = page.viewportSize();
  await page.locator('.tile[data-name="c.png"]').tap();
  await expect(page.locator("#v-name")).toHaveText("c.png");

  await swipe(page, [w * 0.8, h / 2], [w * 0.2, h / 2]);
  await expect(page.locator("#v-name")).toHaveText("b.png");
  await swipe(page, [w * 0.2, h / 2], [w * 0.8, h / 2]);
  await expect(page.locator("#v-name")).toHaveText("c.png");

  await swipe(page, [w / 2, h * 0.3], [w / 2, h * 0.8]);
  await expect(page.locator("#viewer")).toBeHidden();
  await expect(page).toHaveURL(/#\/$/);
});

test("a short drag does nothing", async ({ page, gallery }) => {
  await gallery.open("#/?view=c.png");
  const { width: w, height: h } = page.viewportSize();
  await swipe(page, [w / 2, h / 2], [w / 2 - 20, h / 2 + 20], 600);
  await expect(page.locator("#viewer")).toBeVisible();
  await expect(page.locator("#v-name")).toHaveText("c.png");
});
