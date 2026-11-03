// Records a running coordinator's answers for the docs' demo world, so
// screenshots show real work orders, vision and chats: node capture.mjs
// <project id> <path>... (paths with {p} for the project). It adds to
// captured.json, with the project's ID replaced by the demo one.
import { readFileSync, writeFileSync, existsSync } from "node:fs";

const [real, ...paths] = process.argv.slice(2);
const DEMO = "prj_plantwatertracker";
const file = new URL("./captured.json", import.meta.url);
const captured = existsSync(file) ? JSON.parse(readFileSync(file, "utf8")) : {};
for (const template of paths) {
  const path = template.replaceAll("{p}", real);
  const response = await fetch(`http://localhost:8080${path}`);
  if (!response.ok) {
    console.error(`${response.status} ${path}`);
    continue;
  }
  const text = (await response.text()).replaceAll(real, DEMO);
  captured[template.replaceAll("{p}", DEMO)] = JSON.parse(text);
  console.log(`ok ${path}`);
}
writeFileSync(file, JSON.stringify(captured, null, 1) + "\n");
