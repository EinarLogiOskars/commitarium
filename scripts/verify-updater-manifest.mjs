#!/usr/bin/env node

import { readFileSync, writeFileSync } from "node:fs";
import { pathToFileURL } from "node:url";

const requiredPlatforms = [
  "darwin-aarch64",
  "darwin-x86_64",
  "linux-x86_64",
  "windows-x86_64",
];

// Installed apps download updates from these URLs without GitHub API headers,
// so they must be plain release downloads: an API asset URL answers with JSON
// describing the file instead of the file.
export function releaseDownloadPrefix(repository, tag) {
  return `https://github.com/${repository}/releases/download/${tag}/`;
}

// rewriteUpdaterUrls points every platform at its release download, given the
// release's assets ({ name, apiUrl }) as `gh release view --json assets` lists
// them.
export function rewriteUpdaterUrls(manifest, assets, repository, tag) {
  const names = new Map(assets.map((asset) => [asset.apiUrl, asset.name]));
  for (const entry of Object.values(manifest.platforms ?? {})) {
    const name = names.get(entry.url);
    if (name) entry.url = releaseDownloadPrefix(repository, tag) + encodeURIComponent(name);
  }
  return manifest;
}

export function verifyUpdaterManifest(manifest, expectedVersion, repository) {
  const errors = [];
  const version = expectedVersion.replace(/^v/, "");
  const tag = `v${version}`;

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
    } else if (repository && !entry.url.startsWith(releaseDownloadPrefix(repository, tag))) {
      errors.push(`${platform} does not download from the ${tag} release`);
    }
  }

  return errors;
}

// rewrite <latest.json> <assets.json> <owner/repo> <tag> points the manifest at
// release downloads in place; assets.json is `gh release view --json assets`.
function rewrite(args) {
  if (args.length !== 4) {
    console.error("usage: verify-updater-manifest.mjs rewrite <latest.json> <assets.json> <owner/repo> <tag>");
    return 2;
  }
  const [manifestPath, assetsPath, repository, tag] = args;
  const manifest = JSON.parse(readFileSync(manifestPath, "utf8"));
  const { assets } = JSON.parse(readFileSync(assetsPath, "utf8"));
  writeFileSync(manifestPath, `${JSON.stringify(rewriteUpdaterUrls(manifest, assets, repository, tag), null, 2)}\n`);
  console.log(`updater manifest points at ${releaseDownloadPrefix(repository, tag)}`);
  return 0;
}

function main(args) {
  if (args[0] === "rewrite") return rewrite(args.slice(1));
  if (args.length !== 2 && args.length !== 3) {
    console.error("usage: verify-updater-manifest.mjs <latest.json> <version-or-v-tag> [owner/repo]");
    return 2;
  }

  let manifest;
  try {
    manifest = JSON.parse(readFileSync(args[0], "utf8"));
  } catch (error) {
    console.error(`could not read updater manifest: ${error.message}`);
    return 1;
  }

  const errors = verifyUpdaterManifest(manifest, args[1], args[2]);
  if (errors.length > 0) {
    for (const error of errors) console.error(error);
    return 1;
  }

  console.log(`signed updater manifest covers every supported desktop platform`);
  return 0;
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  process.exitCode = main(process.argv.slice(2));
}
