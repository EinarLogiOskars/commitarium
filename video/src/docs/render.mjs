// Renders every docs screenshot (compositions named docs-<id>-<theme>) into
// the website repository, checked out beside this one (or at WEBSITE_DIR):
// node src/docs/render.mjs [id...]
import { execFileSync } from "node:child_process";
import { existsSync, mkdirSync } from "node:fs";
import { resolve } from "node:path";

const website =
  process.env.WEBSITE_DIR ?? new URL("../../../../commitarium-website/", import.meta.url).pathname;
if (!existsSync(resolve(website, "astro.config.mjs"))) {
  console.error(`No website checkout at ${website}: clone commitarium-website beside this repository, or set WEBSITE_DIR.`);
  process.exit(1);
}
const out = resolve(website, "public/screens") + "/";
mkdirSync(out, { recursive: true });
const listed = execFileSync("npx", ["remotion", "compositions", "src/index.ts", "--quiet"], { encoding: "utf8" });
const wanted = process.argv.slice(2);
const ids = listed
  .split(/\s+/)
  .filter((id) => id.startsWith("docs-"))
  .filter((id) => wanted.length === 0 || wanted.some((w) => id === `docs-${w}-light` || id === `docs-${w}-dark`));
for (const id of ids) {
  const file = `${out}${id.slice("docs-".length)}.png`;
  execFileSync("npx", ["remotion", "still", "src/index.ts", id, file, "--log=error"], { stdio: "inherit" });
  console.log(`rendered ${file}`);
}
