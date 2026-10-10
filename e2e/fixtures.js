// Every test gets its own folder and its own kropka serving it, so a test can
// change files (live reload) without disturbing the others.
//
// The folder, "gallery", sorted newest first:
//   photos/          big.png (large enough to get a real thumbnail)
//   empty/
//   c.png b.png a.png
//   notes.md fetch.py data.bin
//   .secret          hidden, never listed
import { spawn } from "node:child_process";
import { randomBytes } from "node:crypto";
import { mkdir, mkdtemp, rm, utimes, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { crc32, deflateSync } from "node:zlib";
import { test as base, expect } from "@playwright/test";
import { BIN } from "./build.js";

export { expect };

const TOKEN = "e2e-token";
const HOUR = 3600 * 1000;

// A w×h RGB PNG; noise makes it too big to compress (so it gets a thumbnail).
export function png(w, h, [r, g, b], noise = false) {
  const rows = [];
  for (let y = 0; y < h; y++) {
    const row = noise ? randomBytes(1 + w * 3) : Buffer.alloc(1 + w * 3);
    row[0] = 0; // filter byte, then the pixels
    if (!noise) for (let x = 0; x < w; x++) row.set([r, g, b], 1 + x * 3);
    rows.push(row);
  }
  const chunk = (type, data) => {
    const len = Buffer.alloc(4);
    len.writeUInt32BE(data.length);
    const body = Buffer.concat([Buffer.from(type), data]);
    const crc = Buffer.alloc(4);
    crc.writeUInt32BE(crc32(body));
    return Buffer.concat([len, body, crc]);
  };
  const ihdr = Buffer.alloc(13);
  ihdr.writeUInt32BE(w, 0);
  ihdr.writeUInt32BE(h, 4);
  ihdr.set([8, 2, 0, 0, 0], 8); // 8-bit RGB
  return Buffer.concat([
    Buffer.from([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a]),
    chunk("IHDR", ihdr),
    chunk("IDAT", deflateSync(Buffer.concat(rows))),
    chunk("IEND", Buffer.alloc(0)),
  ]);
}

const NOTES = `# Notes

[Jump to the end](#the-end)

| Planet | Moons |
|---|---|
| Mars | 2 |

${"Filler paragraph.\n\n".repeat(80)}
## The end
`;

const FETCH = `import sys

def main():
    print("hello", sys.argv)
`;

// [name, contents, hours after the oldest]
const FILES = [
  ["a.png", () => png(40, 30, [200, 40, 40]), 0],
  ["b.png", () => png(30, 40, [40, 200, 40]), 1],
  ["c.png", () => png(40, 40, [40, 40, 200]), 2],
  ["data.bin", () => Buffer.from([0, 1, 2, 3]), 3],
  ["fetch.py", () => FETCH, 4],
  ["notes.md", () => NOTES, 5],
  ["photos/big.png", () => png(200, 200, [0, 0, 0], true), 6],
  [".secret", () => "hidden", 7],
];

export const test = base.extend({
  // { dir, url, open(hash) }: open logs in with the token and goes to hash.
  gallery: async ({ page }, use) => {
    const tmp = await mkdtemp(join(tmpdir(), "kropka-e2e-"));
    const dir = join(tmp, "gallery");
    await mkdir(join(dir, "photos"), { recursive: true });
    await mkdir(join(dir, "empty"));
    const t0 = new Date(Date.now() - 10 * 24 * HOUR);
    for (const [name, data, h] of FILES) {
      const file = join(dir, name);
      await writeFile(file, data());
      const t = new Date(t0.getTime() + h * HOUR);
      await utimes(file, t, t);
    }

    const proc = spawn(BIN, [
      "--port", "0", "--token", TOKEN, "--quiet", "--ffmpeg", "off",
      "--cache-dir", join(tmp, "cache"), dir,
    ], { stdio: ["ignore", "pipe", "pipe"] });
    let stderr = "";
    proc.stderr.on("data", (d) => { stderr += d; });
    const exited = new Promise((resolve) => proc.on("exit", resolve));
    const url = await new Promise((resolve, reject) => {
      let out = "";
      proc.stdout.on("data", (d) => {
        out += d;
        const m = out.match(/→ (http:\/\/\S+?)\/\?t=/);
        if (m) resolve(m[1]);
      });
      exited.then((code) => reject(new Error(`kropka exited (${code}): ${stderr}`)));
    });

    const open = async (hash = "#/") => {
      await page.goto(`${url}/?t=${TOKEN}${hash}`);
      // A listing hides #status but leaves its text, so check both.
      await expect(page.locator("#status:not([hidden])", { hasText: "Loading…" })).toHaveCount(0);
    };

    await use({ dir, url, open });

    proc.kill();
    await exited;
    await rm(tmp, { recursive: true, force: true });
  },
});
