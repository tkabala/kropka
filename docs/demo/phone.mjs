// Records the phone half of the demo: a phone-sized Chromium that "scans" the
// QR code kropka prints, then browses the folder with real touch input
// (taps, swipes, a pinch), sent over CDP so the page's own gesture code runs.
// A dot follows each finger so the viewer can see the gestures.
//
// It runs alongside demo.tape, which starts kropka and stops it with Ctrl+C
// after a fixed time: the story has to be done by then (record.sh checks).
// It keeps filming until kropka is gone.
//
// The screen is captured with screenshots in a loop, at device resolution
// (Playwright's video and CDP's screencast are both CSS pixels, so blurry):
// out/phone/ gets the JPEGs and an ffconcat list with their timestamps, which
// record.sh turns into a video.
//
// Env: KROPKA_URL, OUT, HEADED=1 to watch it run.
// `phone.mjs --warm` only opens the folder once, see below.

import { chromium, devices } from "playwright";
import { mkdir, readFile, rm, stat, writeFile } from "node:fs/promises";
import { join } from "node:path";

const URL_ = process.env.KROPKA_URL ?? "http://192.168.1.42:8080/?t=demo";
const OUT = process.env.OUT ?? "out";

const device = devices["iPhone 15 Pro"]; // 393 x 659 viewport
const { width: W, height: H } = device.viewport;
const SCALE = 2; // device pixels per CSS pixel

const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

// Draws a dot under each finger. Runs in the page, before its own scripts.
function fingerDots() {
  const dots = new Map();
  const place = (t) => {
    let d = dots.get(t.identifier);
    if (!d) {
      d = document.createElement("div");
      Object.assign(d.style, {
        position: "fixed", zIndex: "2147483647", pointerEvents: "none",
        width: "44px", height: "44px", margin: "-22px 0 0 -22px", borderRadius: "50%",
        background: "rgba(255,255,255,.35)", border: "2px solid rgba(255,255,255,.8)",
        boxShadow: "0 0 12px rgba(0,0,0,.4)", transition: "opacity .25s, transform .25s",
      });
      document.documentElement.append(d);
      dots.set(t.identifier, d);
    }
    d.style.left = t.clientX + "px";
    d.style.top = t.clientY + "px";
  };
  const lift = (e) => {
    for (const t of e.changedTouches) {
      const d = dots.get(t.identifier);
      if (!d) continue;
      dots.delete(t.identifier);
      d.style.opacity = "0";
      d.style.transform = "scale(1.6)";
      setTimeout(() => d.remove(), 300);
    }
  };
  const opts = { capture: true, passive: true };
  addEventListener("touchstart", (e) => { for (const t of e.changedTouches) place(t); }, opts);
  addEventListener("touchmove", (e) => { for (const t of e.changedTouches) place(t); }, opts);
  addEventListener("touchend", lift, opts);
  addEventListener("touchcancel", lift, opts);
}

// The phone's camera, pointed at the terminal, over the page while it loads:
// the terminal's QR code (the real one, see _qr) in a viewfinder, then the
// "open this link" pill a phone shows for a QR code. Tapping the pill reveals
// the page. Also the toast for the zip download.
function phoneChrome({ url, qr }) {
  const host = new URL(url).hostname;
  const css = `
    #cam { position: fixed; inset: 0; z-index: 2147483646; overflow: hidden;
      background: radial-gradient(ellipse at 50% 45%, #2a2a3d 0%, #1e1e2e 45%, #07070a 100%);
      display: grid; place-items: center; transition: opacity .35s, transform .35s;
      font: 15px -apple-system, system-ui, sans-serif; color: #fff; }
    #cam.gone { opacity: 0; transform: scale(1.08); }
    #cam > * { grid-area: 1 / 1; }
    #cam-scene { display: grid; justify-items: start; gap: 12px; color: #cdd6f4;
      font: 11px ui-monospace, "JetBrains Mono", monospace; white-space: pre;
      filter: blur(.5px) brightness(.92); animation: shake 2.6s ease-in-out infinite alternate;
      transform: perspective(700px) rotateX(9deg) rotateZ(-4deg); }
    #cam-scene img { width: 190px; height: 190px; }
    @keyframes shake { 50% { translate: 2px -2px; } 100% { translate: -2px 1px; } }
    #cam-frame { width: 230px; height: 230px; position: relative; transition: transform .3s; }
    #cam.found #cam-frame { transform: scale(.9) rotate(-4deg); }
    #cam-frame i { position: absolute; width: 38px; height: 38px; border: 0 solid #ffd60a; }
    #cam-frame i:nth-child(1) { top: 0; left: 0; border-width: 4px 0 0 4px; border-radius: 14px 0 0 0; }
    #cam-frame i:nth-child(2) { top: 0; right: 0; border-width: 4px 4px 0 0; border-radius: 0 14px 0 0; }
    #cam-frame i:nth-child(3) { bottom: 0; left: 0; border-width: 0 0 4px 4px; border-radius: 0 0 0 14px; }
    #cam-frame i:nth-child(4) { bottom: 0; right: 0; border-width: 0 4px 4px 0; border-radius: 0 0 14px 0; }
    #cam-hint { position: absolute; bottom: 110px; opacity: .75; }
    #cam-pill { position: absolute; top: 150px; padding: 10px 18px; border-radius: 22px;
      background: #ffd60a; color: #000; font-weight: 600; opacity: 0;
      transform: translateY(8px) scale(.9); transition: opacity .25s, transform .25s; }
    #cam.found #cam-pill { opacity: 1; transform: none; }
    #dl { position: fixed; z-index: 2147483646; top: 64px; left: 50%; transform: translate(-50%, -12px);
      padding: 10px 16px; border-radius: 20px; background: rgba(44,44,48,.96); color: #fff;
      font: 14px -apple-system, system-ui, sans-serif; white-space: nowrap; opacity: 0;
      box-shadow: 0 6px 24px rgba(0,0,0,.5); transition: opacity .25s, transform .25s; }
    #dl.on { opacity: 1; transform: translate(-50%, 0); }
    #dl b { font-weight: 600; } #dl span { opacity: .7; }`;
  addEventListener("DOMContentLoaded", () => {
    document.head.append(Object.assign(document.createElement("style"), { textContent: css }));
    const cam = document.createElement("div");
    cam.id = "cam";
    cam.innerHTML = `<div id="cam-scene"><span>→ ${url}</span><img src="${qr}">
      <span>${url}</span></div>
      <div id="cam-frame"><i></i><i></i><i></i><i></i></div>
      <div id="cam-pill">Open ${host}</div><div id="cam-hint">Point at the QR code</div>`;
    document.body.append(cam);
    cam.querySelector("#cam-pill").addEventListener("touchend", () => {
      cam.classList.add("gone");
      setTimeout(() => cam.remove(), 400);
    });
  });
  window.__qrFound = () => document.getElementById("cam").classList.add("found");
  window.__download = (html) => {
    let t = document.getElementById("dl");
    if (!t) {
      t = Object.assign(document.createElement("div"), { id: "dl" });
      document.body.append(t);
      requestAnimationFrame(() => requestAnimationFrame(() => t.classList.add("on")));
    }
    t.innerHTML = html;
  };
}

const FRAMES = join(OUT, "phone");
await rm(FRAMES, { recursive: true, force: true });
await mkdir(FRAMES, { recursive: true });

const browser = await chromium.launch({
  headless: !process.env.HEADED,
  args: ["--autoplay-policy=no-user-gesture-required"],
});

const up = () => fetch(URL_, { redirect: "manual", signal: AbortSignal.timeout(1000) }).then(() => true, () => false);
while (!(await up())) await sleep(100);

// --warm: just open the folder, so kropka makes the thumbnails before the
// recording, which shouldn't wait for them.
if (process.argv[2] === "--warm") {
  const page = await browser.newPage(device);
  await page.goto(URL_);
  await page.locator(".tile img, .tile video").first().waitFor();
  await page.waitForFunction(() => [...document.querySelectorAll(".tile img")].every((i) => i.complete));
  await browser.close();
  process.exit(0);
}

const ctx = await browser.newContext({
  ...device,
  deviceScaleFactor: SCALE,
  colorScheme: "dark",
  acceptDownloads: true,
  bypassCSP: true, // for phoneChrome's <style>
});
await ctx.addInitScript(fingerDots);
const qr = "data:image/png;base64," + (await readFile(join(OUT, "qr.png"))).toString("base64");
await ctx.addInitScript(phoneChrome, { url: URL_, qr });
const page = await ctx.newPage();
const cdp = await ctx.newCDPSession(page);

// ---------- capture ----------

// Its own CDP session, so screenshots don't queue up behind touch events.
const frames = []; // { file, t } with t in ms since the epoch
let capturing = true;
const startCapture = async (shots) => {
  const clip = { x: 0, y: 0, width: W, height: H, scale: SCALE };
  while (capturing) {
    const start = Date.now();
    const { data } = await shots.send("Page.captureScreenshot", { format: "jpeg", quality: 90, clip, optimizeForSpeed: true });
    const file = `${String(frames.length).padStart(5, "0")}.jpg`;
    frames.push({ file, t: (start + Date.now()) / 2 });
    await writeFile(join(FRAMES, file), Buffer.from(data, "base64"));
  }
};

// ---------- touch ----------

const touch = (type, pts) =>
  cdp.send("Input.dispatchTouchEvent", {
    type,
    touchPoints: pts.map(([x, y], id) => ({ x, y, id, radiusX: 10, radiusY: 10, force: 1 })),
  });

const lerp = (a, b, k) => a + (b - a) * k;
const ease = (k) => (k < 0.5 ? 2 * k * k : 1 - (-2 * k + 2) ** 2 / 2);

// Moves fingers from `from` to `to` (arrays of [x, y]) over ms.
async function gesture(from, to, ms = 350) {
  await touch("touchStart", from);
  const steps = Math.max(2, Math.round(ms / 16));
  for (let i = 1; i <= steps; i++) {
    const k = ease(i / steps);
    await touch("touchMove", from.map(([x, y], j) => [lerp(x, to[j][0], k), lerp(y, to[j][1], k)]));
    await sleep(ms / steps);
  }
  await touch("touchEnd", []);
}

const swipe = (from, to, ms) => gesture([from], [to], ms);

async function tap([x, y]) {
  await touch("touchStart", [[x, y]]);
  await sleep(70);
  await touch("touchEnd", []);
}

async function center(selector) {
  const loc = page.locator(selector).first();
  await loc.waitFor();
  const b = await loc.boundingBox();
  return [b.x + b.width / 2, b.y + b.height / 2];
}

// A gesture can misfire when the machine is busy, and a viewer left open
// would derail the rest.
async function close() {
  await tap(await center("#v-close"));
  await sleep(400);
  if (await page.locator("#viewer").isVisible()) await tap(await center("#v-close"));
}

async function swipeClose() {
  await swipe([W / 2, H * 0.4], [W / 2, H * 0.85], 300);
  await sleep(600);
  if (await page.locator("#viewer").isVisible()) await tap(await center("#v-close"));
}

// Scrolls the page by a finger drag; dy > 0 scrolls down.
const scroll = (dy, x = W / 2) => swipe([x, H / 2 + dy / 2], [x, H / 2 - dy / 2], 450);

// When each step starts, printed at the end: the tape's Sleep has to cover it.
const steps = [];
const step = (label) => steps.push([label, Date.now()]);

const tile = (name) => `.tile[data-name="${name}"]`;
const file = (name) => `.file[data-name="${name}"]`;

// ---------- story ----------

// Load the page behind the camera, then start filming: screenshots hang
// across a navigation. record.sh holds the first frame until then.
await page.goto(URL_);
await page.locator(".tile img").first().waitFor();
const capture = startCapture(await ctx.newCDPSession(page));
await sleep(700);

// The camera finds the code; open it.
step("scan");
await page.evaluate(() => window.__qrFound());
await sleep(1000);
await tap(await center("#cam-pill"));
await sleep(1100);

// Aldrin on the Moon: pinch into his visor, where Armstrong is reflected,
// and bring it to the middle.
step("photo");
await tap(await center(tile("aldrin-apollo-11.jpg")));
await page.locator("#v-stage img").waitFor();
await sleep(900);
const img = await page.locator("#v-stage img").boundingBox();
const [vx, vy] = [img.x + img.width * 0.52, img.y + img.height * 0.16];
await gesture([[vx - 30, vy], [vx + 30, vy]], [[vx - 105, vy], [vx + 105, vy]], 800);
await sleep(200);
await swipe([vx, vy + 60], [vx, H / 2 + 60], 600);
await sleep(1500);
await gesture([[W / 2 - 150, H / 2], [W / 2 + 150, H / 2]], [[W / 2 - 30, H / 2], [W / 2 + 30, H / 2]], 600);
await sleep(600);

// Swipe on to the next one, then down to close.
await swipe([W * 0.8, H / 2], [W * 0.15, H / 2], 260);
await sleep(1100);
await swipeClose();
await sleep(300);

// The rollout timelapse plays.
step("video");
await tap(await center(tile("artemis-rollout.webm")));
await sleep(3200);
await close();
await sleep(400);

// The notes, rendered, and the script, highlighted.
step("notes");
await tap(await center(file("notes.md")));
await page.locator(".doc table").waitFor();
await sleep(1300);
await scroll(260);
await sleep(1000);
await close();
await sleep(300);
step("code");
await tap(await center(file("fetch.py")));
await page.locator(".doc").waitFor();
await sleep(1500);
await close();
await sleep(400);

// The whole folder as a zip.
step("zip");
const download = page.waitForEvent("download");
await tap(await center("#zip"));
const dl = await download;
const name = dl.suggestedFilename();
await page.evaluate((n) => window.__download(`⬇&#xFE0E; <b>${n}</b>`), name);
const mb = (await stat(await dl.path())).size / 1e6;
await page.evaluate(([n, s]) => window.__download(`✓ <b>${n}</b> <span>· ${s} MB</span>`), [name, mb.toFixed(1)]);
step("done");
const storyEnd = Date.now();

// Film until Ctrl+C stops kropka, and a moment after.
while (await up()) await sleep(200);
await sleep(800);

// ---------- save ----------

capturing = false;
await capture;
const end = Date.now();
await browser.close();
const list = ["ffconcat version 1.0"];
frames.forEach((f, i) => {
  const next = i + 1 < frames.length ? frames[i + 1].t : end;
  list.push(`file ${f.file}`, `duration ${((next - f.t) / 1000).toFixed(3)}`);
});
list.push(`file ${frames.at(-1).file}`); // the last duration only counts if a file follows
await writeFile(join(FRAMES, "frames.ffconcat"), list.join("\n") + "\n");
await writeFile(join(OUT, "phone.t0"), String(Math.round(frames[0].t)));
await writeFile(join(OUT, "phone.done"), String(storyEnd));
const t0 = frames[0].t;
console.log("phone: " + steps.map(([l, t]) => `${l} ${((t - t0) / 1000).toFixed(1)}s`).join(", "));
console.log(`phone: ${frames.length} frames, ${((end - frames[0].t) / 1000).toFixed(1)}s, story done after ${((storyEnd - frames[0].t) / 1000).toFixed(1)}s`);
