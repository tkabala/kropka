import { mkdir, writeFile } from "node:fs/promises";
import { join } from "node:path";
import { expect, png, test } from "../fixtures.js";

const viewer = (page) => page.locator("#viewer");

test("a tap opens the viewer, close and back return to the folder", async ({ page, gallery }) => {
  await gallery.open();
  await page.locator('.tile[data-name="b.png"]').click();
  await expect(viewer(page)).toBeVisible();
  await expect(page).toHaveURL(/#\/\?view=b\.png$/);
  await expect(page.locator("#v-count")).toHaveText("2 / 5");
  await expect(page.locator("#v-name")).toHaveText("b.png");
  await expect(page.locator("#v-download")).toHaveAttribute("href", "/raw/b.png?dl=1");
  await expect(page.locator("#v-stage img")).toHaveAttribute("src", "/raw/b.png");
  await expect(page.locator("body")).toHaveClass(/locked/);

  await page.locator("#v-close").click();
  await expect(viewer(page)).toBeHidden();
  await expect(page).toHaveURL(/#\/$/);
  await expect(page.locator("body")).not.toHaveClass(/locked/);

  await page.locator('.tile[data-name="b.png"]').click();
  await expect(viewer(page)).toBeVisible();
  await page.goBack();
  await expect(viewer(page)).toBeHidden();

  await page.locator('.tile[data-name="b.png"]').click();
  await expect(viewer(page)).toBeVisible();
  await page.keyboard.press("Escape");
  await expect(viewer(page)).toBeHidden();
  await expect(page).toHaveURL(/#\/$/);
});

test("steps through media, then text files, in page order", async ({ page, gallery }) => {
  await gallery.open();
  await page.locator('.tile[data-name="c.png"]').click();
  await expect(page.locator("#v-prev")).toBeHidden();

  const seen = [];
  for (let i = 0; i < 5; i++) {
    seen.push(await page.locator("#v-name").textContent());
    if (i < 4) await page.locator("#v-next").click();
  }
  expect(seen).toEqual(["c.png", "b.png", "a.png", "notes.md", "fetch.py"]);
  await expect(page.locator("#v-next")).toBeHidden();
  await expect(page).toHaveURL(/\?view=fetch\.py$/);

  await page.keyboard.press("ArrowLeft");
  await expect(page.locator("#v-name")).toHaveText("notes.md");
  await page.keyboard.press("ArrowRight");
  await expect(page.locator("#v-name")).toHaveText("fetch.py");
  await page.keyboard.press("ArrowRight"); // past the end: nothing
  await expect(page.locator("#v-name")).toHaveText("fetch.py");

  // Stepping replaces history, so back leaves the viewer in one go.
  await page.goBack();
  await expect(viewer(page)).toBeHidden();
});

test("a link to a file opens it, a stale one falls back to the folder", async ({ page, gallery }) => {
  await gallery.open("#/?view=a.png");
  await expect(viewer(page)).toBeVisible();
  await expect(page.locator("#v-count")).toHaveText("3 / 5");
  // Opened by a link, not a tap: closing must not leave the page.
  await page.locator("#v-close").click();
  await expect(viewer(page)).toBeHidden();
  await expect(page).toHaveURL(/#\/$/);

  await gallery.open("#/?view=gone.png");
  await expect(viewer(page)).toBeHidden();
  await expect(page).toHaveURL(/#\/$/);
});

test("names that need escaping survive the round trip", async ({ page, gallery }) => {
  const odd = "50% off ?view=#1";
  await mkdir(join(gallery.dir, odd));
  await writeFile(join(gallery.dir, odd, "a b&c.png"), png(10, 10, [9, 9, 9]));
  await gallery.open();
  await page.locator(".folder", { hasText: odd }).click();
  await expect(page.locator("#crumbs a").last()).toHaveText(odd);
  await page.locator(".tile").click();
  await expect(page.locator("#v-name")).toHaveText("a b&c.png");
  await expect.poll(() => page.locator("#v-stage img").evaluate((i) => i.complete && i.naturalWidth)).toBe(10);

  await page.reload();
  await expect(page.locator("#v-name")).toHaveText("a b&c.png");
  await expect(page.locator("#crumbs a").last()).toHaveText(odd);
});

test("Markdown is rendered, and #anchors scroll", async ({ page, gallery }) => {
  await gallery.open("#/?view=notes.md");
  const doc = page.locator("#v-stage .doc .markdown");
  await expect(doc.locator("h1")).toHaveText("Notes");
  await expect(doc.locator("table td").first()).toHaveText("Mars");

  const end = page.locator("#md-the-end");
  await expect(end).not.toBeInViewport();
  await doc.getByRole("link", { name: "Jump to the end" }).click();
  await expect(end).toBeInViewport();
  await expect(page).toHaveURL(/\?view=notes\.md$/);
});

test("code is highlighted", async ({ page, gallery }) => {
  await gallery.open("#/?view=fetch.py");
  const pre = page.locator("#v-stage .doc pre.chroma");
  await expect(pre).toContainText('print("hello", sys.argv)');
  await expect(pre.locator(".kn", { hasText: "import" })).toHaveCount(1);
});

test("images zoom with the keyboard and a double click", async ({ page, gallery }) => {
  await page.setViewportSize({ width: 400, height: 400 });
  await writeFile(join(gallery.dir, "wide.png"), png(1600, 1000, [90, 90, 90]));
  await gallery.open("#/?view=wide.png");
  const img = page.locator("#v-stage img");
  await expect.poll(() => img.evaluate((i) => i.complete && i.offsetWidth)).toBeGreaterThan(0);

  await page.keyboard.press("+");
  await expect(img).toHaveCSS("transform", /^matrix\(1\.5, 0, 0, 1\.5,/);
  await expect(img).toHaveClass(/zoomed/);
  await page.keyboard.press("0");
  await expect(img).toHaveCSS("transform", "none");

  await img.dblclick();
  await expect(img).toHaveCSS("transform", /^matrix\(2\.5, 0, 0, 2\.5,/);
  await img.dblclick();
  await expect(img).toHaveCSS("transform", "none");
});
