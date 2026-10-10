// Builds the kropka binary the tests run, once per `playwright test`.
import { execFileSync } from "node:child_process";
import { fileURLToPath } from "node:url";

export const BIN = fileURLToPath(new URL(".bin/kropka" + (process.platform === "win32" ? ".exe" : ""), import.meta.url));

export default function build() {
  execFileSync("go", ["build", "-o", BIN, "./cmd/kropka"], {
    cwd: fileURLToPath(new URL("..", import.meta.url)),
    stdio: "inherit",
  });
}
