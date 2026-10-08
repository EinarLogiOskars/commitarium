import assert from "node:assert/strict";
import test from "node:test";

import { verifyUpdaterManifest } from "./verify-updater-manifest.mjs";

function validManifest() {
  const platforms = {};
  for (const platform of [
    "darwin-aarch64",
    "darwin-x86_64",
    "linux-x86_64",
    "windows-x86_64",
  ]) {
    platforms[platform] = {
      signature: `signature-${platform}`,
      url: `https://api.github.com/repos/example/project/releases/assets/${platform}`,
    };
  }
  return {
    version: "0.5.0",
    notes: "Release notes",
    pub_date: "2026-10-08T12:00:00.000Z",
    platforms,
  };
}

test("accepts complete signed platform coverage", () => {
  assert.deepEqual(verifyUpdaterManifest(validManifest(), "v0.5.0"), []);
});

test("rejects a stale version and missing platform", () => {
  const manifest = validManifest();
  manifest.version = "0.4.0";
  delete manifest.platforms["windows-x86_64"];
  assert.deepEqual(verifyUpdaterManifest(manifest, "0.5.0"), [
    "manifest version is 0.4.0; expected 0.5.0",
    "manifest is missing windows-x86_64",
  ]);
});

test("rejects unsigned or insecure platform entries", () => {
  const manifest = validManifest();
  manifest.platforms["linux-x86_64"].signature = "";
  manifest.platforms["windows-x86_64"].url = "http://example.test/update";
  assert.deepEqual(verifyUpdaterManifest(manifest, "0.5.0"), [
    "linux-x86_64 has no update signature",
    "windows-x86_64 has no secure download URL",
  ]);
});
