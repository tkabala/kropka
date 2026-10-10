import { rm, writeFile } from "node:fs/promises";
import { join } from "node:path";
import { expect, png, test } from "../fixtures.js";

test("new and deleted files show up without a refresh", async ({ page, gallery }) => {
  await gallery.open();
  await writeFile(join(gallery.dir, "new.png"), png(10, 10, [1, 2, 3]));
  await expect(page.locator('.tile[data-name="new.png"]')).toBeVisible();
  // Newest first, so it leads.
  await expect(page.locator(".tile").first()).toHaveAttribute("data-name", "new.png");

  await rm(join(gallery.dir, "a.png"));
  await expect(page.locator('.tile[data-name="a.png"]')).toHaveCount(0);
});

test("the viewer stays on its file as the folder changes", async ({ page, gallery }) => {
  await gallery.open("#/?view=b.png");
  await expect(page.locator("#v-count")).toHaveText("2 / 5");
  await writeFile(join(gallery.dir, "new.png"), png(10, 10, [1, 2, 3]));
  await expect(page.locator("#v-count")).toHaveText("3 / 6");
  await expect(page.locator("#v-name")).toHaveText("b.png");

  // Its file is deleted: it moves on to what took its place.
  await rm(join(gallery.dir, "b.png"));
  await expect(page.locator("#v-count")).toHaveText("3 / 5");
  await expect(page.locator("#v-name")).toHaveText("a.png");
  await expect(page).toHaveURL(/\?view=a\.png$/);
});

test("only the folder on screen is watched", async ({ page, gallery }) => {
  await gallery.open("#/photos");
  await writeFile(join(gallery.dir, "elsewhere.png"), png(10, 10, [1, 2, 3]));
  await writeFile(join(gallery.dir, "photos", "here.png"), png(10, 10, [1, 2, 3]));
  await expect(page.locator(".tile")).toHaveCount(2);
  await page.locator("#crumbs a", { hasText: "gallery" }).click();
  await expect(page.locator('.tile[data-name="elsewhere.png"]')).toBeVisible();
});
