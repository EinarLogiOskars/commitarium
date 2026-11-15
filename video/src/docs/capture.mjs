// Records a running coordinator's answers for the docs' demo world, so
// screenshots show real work orders, vision and chats: node capture.mjs
// <project id> <path>... (paths with {p} for the project). It adds to
// captured.json, with the project's ID replaced by the demo one. It sends
// the coordinator's token from COMMITARIUM_COORDINATOR_TOKEN_FILE, or the
// checkout's .commitarium/internal/coordinator-token.
import { readFileSync, writeFileSync, existsSync } from "node:fs";

const [real, ...paths] = process.argv.slice(2);
const tokenFile =
  process.env.COMMITARIUM_COORDINATOR_TOKEN_FILE ??
  new URL("../../../.commitarium/internal/coordinator-token", import.meta.url);
const token = readFileSync(tokenFile, "utf8").trim();
const DEMO = "prj_plantwatertracker";
const file = new URL("./captured.json", import.meta.url);
const captured = existsSync(file) ? JSON.parse(readFileSync(file, "utf8")) : {};
for (const template of paths) {
  const path = template.replaceAll("{p}", real);
  const response = await fetch(`http://localhost:8080${path}`, {
    headers: { Authorization: `Bearer ${token}` },
  });
  if (!response.ok) {
    console.error(`${response.status} ${path}`);
    continue;
  }
  const text = (await response.text()).replaceAll(real, DEMO);
  captured[template.replaceAll("{p}", DEMO)] = JSON.parse(text);
  console.log(`ok ${path}`);
}
writeFileSync(file, JSON.stringify(captured, null, 1) + "\n");
