# Commitarium desktop

This directory contains the Tauri desktop application and its React UI. The
renderer talks to the local coordinator through its versioned HTTP API and can
invoke only the narrow native commands registered in `src-tauri/src/lib.rs`.

## Trusted local handoff

The native command `synchronize_feature_locally` transfers one completed work
order from internal Forgejo into the Git repository from which its project was
imported. Invoke it with camel-cased Tauri arguments:

```ts
invoke("synchronize_feature_locally", {
  projectId,
  featureId,
  commitMessage,
});
```

It returns:

```ts
{
  project_id: string;
  feature_id: string;
  repository_path: string;
  target_branch: string;
  local_commit_id: string;
  created: boolean;
}
```

The command requires the recorded source repository to be clean, on the
project's default branch, and configured with `user.name` and `user.email`. It
fetches the coordinator's exact completed-handoff description, retrieves the
reviewed internal Git objects using the local Forgejo token, and prepares one
new commit in a temporary checkout. The commit is installed only if its tree is
identical to the approved Forgejo tree and the local branch has not advanced.

Trusted receipts are stored in the app data directory as
`handoff-receipts.json`. They map internal merge history to the deliberately
different clean local commits, which permits later work orders to form a normal
local chain and makes exact retries safe.

This command never pushes. Upstream publication remains a separate future
native command.

Projects imported from a plain folder use a separate command because there is
no local Git history in which to create a clean commit:

```ts
invoke("synchronize_feature_to_folder", {
  projectId,
  featureId,
});
```

It returns:

```ts
{
  project_id: string;
  feature_id: string;
  folder_path: string;
  result_tree_id: string;
  created: boolean;
}
```

The command temporarily gives the folder a Git index stored outside the
project; it never creates `.git` in the user's folder. Before writing, the
current non-ignored content must exactly match the work order's internal base.
The reviewed binary-safe patch is checked, a durable prepared receipt is saved,
and the result must exactly match the approved Forgejo tree. Ignored files such
as dependency caches and build output are left untouched. A changed or
partially updated folder stops for user inspection instead of overwriting or
retrying uncertain work.

Plain-folder receipts are stored in the app data directory as
`folder-handoff-receipts.json`. An exact retry reports `created: false`, as does
recovery when the folder reached the approved tree before the completed receipt
was saved.

During repository development the internal token is discovered at
`.commitarium/forgejo-token`. Packaged or relocated installations can set
`COMMITARIUM_FORGEJO_TOKEN_FILE` to the token file's absolute path.

## Development

From this directory:

```sh
pnpm install
pnpm tauri dev
```

Useful verification commands:

```sh
pnpm build
cargo test --manifest-path src-tauri/Cargo.toml
cargo clippy --manifest-path src-tauri/Cargo.toml --all-targets -- -D warnings
```
