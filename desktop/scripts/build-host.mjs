#!/usr/bin/env node
// Builds commitarium-host where Tauri's bundler expects its sidecar:
// src-tauri/binaries/commitarium-host-<target triple>. The build commands in
// tauri.conf.json run this, and CI runs it before anything compiles the
// desktop crate, because tauri-build refuses a missing sidecar.

import { execFileSync } from "node:child_process";
import { copyFileSync, mkdirSync, readFileSync, existsSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const crate = join(dirname(fileURLToPath(import.meta.url)), "..", "src-tauri");
const target =
  process.env.TAURI_ENV_TARGET_TRIPLE ||
  execFileSync("rustc", ["--print", "host-tuple"], { encoding: "utf8" }).trim();
// Tauri sets the triple for its builds, and TAURI_ENV_DEBUG only for debug ones.
const release = Boolean(process.env.TAURI_ENV_TARGET_TRIPLE) && !process.env.TAURI_ENV_DEBUG;
const profile = release ? "release" : "debug";
const suffix = target.includes("windows") ? ".exe" : "";

const build = (triple) => {
  const args = ["build", "--locked", "-p", "commitarium-host", "--target", triple];
  execFileSync("cargo", release ? [...args, "--release"] : args, {
    cwd: crate,
    stdio: "inherit",
  });
  return join(crate, "target", triple, profile, `commitarium-host${suffix}`);
};

const destination = join(crate, "binaries", `commitarium-host-${target}${suffix}`);
mkdirSync(dirname(destination), { recursive: true });

if (target === "universal-apple-darwin") {
  const triples = ["aarch64-apple-darwin", "x86_64-apple-darwin"];
  const parts = triples.map(build);
  execFileSync("lipo", ["-create", "-output", destination, ...parts], { stdio: "inherit" });
  // Each architecture's own compile looks for its own sidecar too.
  triples.forEach((triple, index) =>
    copyFileSync(parts[index], join(crate, "binaries", `commitarium-host-${triple}`)),
  );
} else {
  const built = build(target);
  // An unchanged copy keeps `tauri dev` from rebuilding for nothing.
  const same = existsSync(destination) && readFileSync(built).equals(readFileSync(destination));
  if (!same) copyFileSync(built, destination);
}
