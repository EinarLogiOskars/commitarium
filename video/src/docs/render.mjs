// Renders every docs screenshot (compositions named docs-<id>-<theme>) into
// the website: node src/docs/render.mjs [id...]
import { execFileSync } from "node:child_process";
import { mkdirSync } from "node:fs";

const out = new URL("../../../website/public/screens/", import.meta.url).pathname;
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
