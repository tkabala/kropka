import { execFileSync } from "node:child_process";
import { rm } from "node:fs/promises";
import { join } from "node:path";
import { expect, test } from "../fixtures.js";

// The gallery's slides, newest first: c.png b.png a.png, then notes.md and
// fetch.py, which the slideshow skips. A fake clock steps it without waiting.
const SLIDE = 5000;
const name = (page) => page.locator("#v-name");
const play = (page) => page.locator("#v-play");

// Runs the clock to the next slide. The wait starts when an image has loaded,
// which happens on the real clock, so the last half second is stepped until
// the slide changes.
async function nextSlide(page) {
  const before = await name(page).textContent();
  await page.clock.runFor(SLIDE - 500);
  await expect(name(page)).toHaveText(before);
  await expect.poll(async () => {
    await page.clock.runFor(100);
    return name(page).textContent();
  }).not.toBe(before);
}

async function hasFFmpeg() {
  try {
    execFileSync("ffmpeg", ["-version"], { stdio: "ignore" });
    return true;
  } catch {
    return false;
  }
}

test("plays through the images, skips text and loops", async ({ page, gallery }) => {
  await page.clock.install();
  await gallery.open("#/?view=b.png");
  await expect(play(page)).toHaveAttribute("aria-pressed", "false");
  await play(page).click();
  await expect(play(page)).toHaveAttribute("aria-pressed", "true");

  for (const next of ["a.png", "c.png", "b.png"]) {
    await nextSlide(page);
    await expect(name(page)).toHaveText(next);
  }
  await expect(page).toHaveURL(/\?view=b\.png$/);
});

test("starting on a text file jumps to the next image", async ({ page, gallery }) => {
  await page.clock.install();
  await gallery.open("#/?view=notes.md");
  await page.keyboard.press(" ");
  await expect(name(page)).toHaveText("c.png");
  await expect(play(page)).toHaveAttribute("aria-pressed", "true");
});

test("a key, a step or zooming in pauses it", async ({ page, gallery }) => {
  await page.clock.install();
  await gallery.open("#/?view=c.png");

  await page.keyboard.press(" ");
  await page.keyboard.press(" ");
  await expect(play(page)).toHaveAttribute("aria-pressed", "false");
  await page.clock.runFor(SLIDE * 2);
  await expect(name(page)).toHaveText("c.png");

  await page.keyboard.press(" ");
  await page.keyboard.press("ArrowRight");
  await expect(name(page)).toHaveText("b.png");
  await expect(play(page)).toHaveAttribute("aria-pressed", "false");
  await page.clock.runFor(SLIDE * 2);
  await expect(name(page)).toHaveText("b.png");

  await page.keyboard.press(" ");
  await page.locator("#v-stage img").hover();
  await page.mouse.wheel(0, -300);
  await expect(page.locator("#v-stage img")).toHaveClass(/zoomed/);
  await expect(play(page)).toHaveAttribute("aria-pressed", "false");
  await page.clock.runFor(SLIDE * 2);
  await expect(name(page)).toHaveText("b.png");
});

test("closing the viewer stops it", async ({ page, gallery }) => {
  await page.clock.install();
  await gallery.open("#/?view=c.png");
  await play(page).click();
  await page.locator("#v-close").click();
  await page.clock.runFor(SLIDE * 2);
  await expect(page.locator("#viewer")).toBeHidden();
  await expect(page).toHaveURL(/#\/$/);
});

test("no play button with fewer than two images", async ({ page, gallery }) => {
  await rm(join(gallery.dir, "a.png"));
  await rm(join(gallery.dir, "b.png"));
  await gallery.open("#/?view=c.png");
  await expect(name(page)).toHaveText("c.png");
  await expect(play(page)).toBeHidden();
});

test("a video plays to the end before the next slide", async ({ page, gallery }) => {
  test.skip(!(await hasFFmpeg()), "needs ffmpeg to make the clip");
  // Newest, so it comes first: clip.webm, c.png, ...
  execFileSync("ffmpeg", [
    "-loglevel", "error", "-f", "lavfi", "-i", "color=c=red:s=64x64:d=1.5:r=10",
    "-c:v", "libvpx", "-b:v", "100k", join(gallery.dir, "clip.webm"),
  ]);
  await gallery.open("#/?view=a.png");
  await play(page).click();
  // Real timers here: the clip plays in real time, so wait out the image too.
  await expect(name(page)).toHaveText("clip.webm", { timeout: SLIDE + 3000 });
  const video = page.locator("#v-stage video");
  await expect(video).toHaveJSProperty("paused", false);
  await expect(name(page)).toHaveText("c.png", { timeout: 5000 });
  await expect(play(page)).toHaveAttribute("aria-pressed", "true");
});
