import { expect, test } from "../fixtures.js";

const names = (page, sel) => page.locator(sel).evaluateAll((els) =>
  els.map((e) => e.dataset.name ?? (e.querySelector(".file-name") ?? e).textContent.trim()));

test("lists folders, media and files, newest first", async ({ page, gallery }) => {
  await gallery.open();
  await expect(page).toHaveTitle("kropka · gallery");
  await expect(page.locator("#crumbs a")).toHaveText(["gallery"]);
  expect(await names(page, ".folder")).toEqual(["photos", "empty"]);
  expect(await names(page, ".tile")).toEqual(["c.png", "b.png", "a.png"]);
  expect(await names(page, ".file")).toEqual(["notes.md", "fetch.py", "data.bin"]);
  await expect(page.locator(".file", { hasText: "notes.md" }).locator(".ext")).toHaveText("md");
  await expect(page.getByText(".secret")).toHaveCount(0);
  await expect(page.locator("#status")).toBeHidden();
});

test("files the viewer can't show are links", async ({ page, gallery }) => {
  await gallery.open();
  const bin = page.locator("a.file", { hasText: "data.bin" });
  await expect(bin).toHaveAttribute("href", "/raw/data.bin?dl=1");
  await expect(page.locator("a.file")).toHaveCount(1);
});

test("sort order toggles and is remembered", async ({ page, gallery }) => {
  await gallery.open();
  await expect(page.locator("#sort")).toHaveText("Newest");
  await page.locator("#sort").click();
  await expect(page.locator("#sort")).toHaveText("A–Z");
  expect(await names(page, ".folder")).toEqual(["empty", "photos"]);
  expect(await names(page, ".tile")).toEqual(["a.png", "b.png", "c.png"]);
  expect(await names(page, ".file")).toEqual(["data.bin", "fetch.py", "notes.md"]);

  await page.reload();
  await expect(page.locator("#sort")).toHaveText("A–Z");
  expect(await names(page, ".tile")).toEqual(["a.png", "b.png", "c.png"]);
});

test("folders open, and back returns", async ({ page, gallery }) => {
  await gallery.open();
  await page.locator(".folder", { hasText: "photos" }).click();
  await expect(page).toHaveURL(/#\/photos$/);
  await expect(page.locator("#crumbs a")).toHaveText(["gallery", "photos"]);
  await expect(page.locator(".tile")).toHaveCount(1);

  await page.goBack();
  await expect(page.locator(".tile")).toHaveCount(3);
  await page.locator(".folder", { hasText: "photos" }).click();
  await page.locator("#crumbs a", { hasText: "gallery" }).click();
  await expect(page).toHaveURL(/#\/$/);
  await expect(page.locator(".tile")).toHaveCount(3);
});

test("large images get a thumbnail", async ({ page, gallery }) => {
  await gallery.open("#/photos");
  const img = page.locator(".tile img");
  await expect(img).toHaveAttribute("src", /^\/thumb\/photos\/big\.png\?v=\d+$/);
  await expect.poll(() => img.evaluate((i) => i.complete && i.naturalWidth)).toBeGreaterThan(0);
});

test("small images are shown as they are", async ({ page, gallery }) => {
  await gallery.open();
  await expect(page.locator('.tile[data-name="a.png"] img')).toHaveAttribute("src", "/raw/a.png");
});

test("empty and missing folders say so", async ({ page, gallery }) => {
  await gallery.open("#/empty");
  await expect(page.locator("#status")).toHaveText("This folder is empty.");

  await gallery.open("#/nope");
  await expect(page.locator("#status")).toHaveText("This folder doesn’t exist.");
  await expect(page.locator("#grid")).toBeHidden();

  await gallery.open("#/notes.md");
  await expect(page.locator("#status")).toHaveText("This is a file, not a folder.");
});

test("an expired session says what to do", async ({ page, gallery }) => {
  await gallery.open();
  await page.context().clearCookies();
  await page.locator("#refresh").click();
  await expect(page.locator("#status")).toHaveText(/Session expired/);
});

test("the folder downloads as a zip", async ({ page, gallery }) => {
  await gallery.open();
  const download = page.waitForEvent("download");
  await page.locator("#zip").click();
  expect((await download).suggestedFilename()).toBe("gallery.zip");
});
