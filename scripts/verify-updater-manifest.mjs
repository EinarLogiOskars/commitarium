#!/usr/bin/env node

import { readFileSync } from "node:fs";
import { pathToFileURL } from "node:url";

const requiredPlatforms = [
  "darwin-aarch64",
  "darwin-x86_64",
  "linux-x86_64",
  "windows-x86_64",
];

export function verifyUpdaterManifest(manifest, expectedVersion) {
  const errors = [];
  const version = expectedVersion.replace(/^v/, "");

  if (manifest === null || typeof manifest !== "object" || Array.isArray(manifest)) {
    return ["manifest must be a JSON object"];
  }
  if (manifest.version !== version) {
    errors.push(`manifest version is ${String(manifest.version)}; expected ${version}`);
  }
  if (typeof manifest.pub_date !== "string" || Number.isNaN(Date.parse(manifest.pub_date))) {
    errors.push("manifest pub_date is missing or invalid");
  }
  if (manifest.platforms === null || typeof manifest.platforms !== "object") {
    errors.push("manifest platforms object is missing");
    return errors;
  }

  for (const platform of requiredPlatforms) {
    const entry = manifest.platforms[platform];
    if (entry === null || typeof entry !== "object") {
      errors.push(`manifest is missing ${platform}`);
      continue;
    }
    if (typeof entry.signature !== "string" || entry.signature.trim() === "") {
      errors.push(`${platform} has no update signature`);
    }
    if (typeof entry.url !== "string" || !entry.url.startsWith("https://")) {
      errors.push(`${platform} has no secure download URL`);
    }
  }

  return errors;
}

function main(args) {
  if (args.length !== 2) {
    console.error("usage: verify-updater-manifest.mjs <latest.json> <version-or-v-tag>");
    return 2;
  }

  let manifest;
  try {
    manifest = JSON.parse(readFileSync(args[0], "utf8"));
  } catch (error) {
    console.error(`could not read updater manifest: ${error.message}`);
    return 1;
  }

  const errors = verifyUpdaterManifest(manifest, args[1]);
  if (errors.length > 0) {
    for (const error of errors) console.error(error);
    return 1;
  }

  console.log(`signed updater manifest covers every supported desktop platform`);
  return 0;
}

if (import.meta.url === pathToFileURL(process.argv[1]).href) {
  process.exitCode = main(process.argv.slice(2));
}
