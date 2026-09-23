import { useCallback, useEffect, useState } from "react";
import {
  createProjectRemote,
  getGitIdentity,
  getProjectSyncState,
  initializeProjectLocalRepository,
  openExternal,
  pickFolder,
  probeGitProviders,
  synchronizeProjectLocally,
  previewProjectUpstreamBranch,
  publishProjectUpstreamBranch,
  type GitProviderProbe,
  type ProjectSyncState,
  type ProjectUpstreamResult,
} from "../ipc";

const POLL_MS = 5000;

// Project-level handoff: bring the user's machine up to the canonical Forgejo
// default branch (all merged orders), as one clean 3-way commit — not per-order.
// Shows how far behind the local repo / each remote is, and syncs the whole
// project state at once.
export function ProjectSyncCard({
  projectId,
  projectName,
}: {
  projectId: string;
  projectName: string;
}) {
  const [state, setState] = useState<ProjectSyncState | null>(null);
  const [error, setError] = useState<string | null>(null);

  const load = useCallback(async () => {
    try {
      setState(await getProjectSyncState(projectId));
      setError(null);
    } catch (e) {
      setError(String(e));
    }
  }, [projectId]);

  useEffect(() => {
    void load();
    const id = setInterval(() => void load(), POLL_MS);
    return () => clearInterval(id);
  }, [load]);

  if (error && !state) {
    return (
      <section className="panel">
        <h2>Sync to your machine</h2>
        <div className="banner banner--error">{error}</div>
      </section>
    );
  }
  if (!state) {
    return (
      <section className="panel">
        <h2>Sync to your machine</h2>
        <p className="muted">Checking sync state…</p>
      </section>
    );
  }

  const isGit = state.source?.sourceType === "git";
  const creatingFirstRemote = Boolean(
    isGit &&
    state.source?.createdByCommitarium &&
    state.local.watermarkCommitId === state.canonical.headCommitId &&
    !state.upstreams.some((upstream) => upstream.watermarkCommitId != null),
  );

  return (
    <section className="panel">
      <div className="panel__head">
        <h2>Sync to your machine</h2>
        <span className="muted handoff__meta">
          canonical {state.canonical.defaultBranch} @ {state.canonical.headCommitId.slice(0, 12)}
        </span>
      </div>
      {error && <div className="banner banner--error">{error}</div>}

      {!state.source ? (
        <WorkspaceSetup state={state} projectName={projectName} onDone={load} />
      ) : (
        <LocalSync state={state} onDone={load} />
      )}

      {isGit && state.upstreams.length > 0 && !creatingFirstRemote && (
        <Push state={state} onDone={load} />
      )}
      {creatingFirstRemote && (
        <CreateRemote state={state} projectName={projectName} onDone={load} />
      )}
    </section>
  );
}

function WorkspaceSetup({
  state,
  projectName,
  onDone,
}: {
  state: ProjectSyncState;
  projectName: string;
  onDone: () => void;
}) {
  const [parent, setParent] = useState("");
  const [folder, setFolder] = useState(slug(projectName));
  const [name, setName] = useState("");
  const [email, setEmail] = useState("");
  const [message, setMessage] = useState(`Initialize ${projectName}`);
  const [key] = useState(newIdempotencyKey);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    void getGitIdentity().then((identity) => {
      setName((current) => current || identity.name || "");
      setEmail((current) => current || identity.email || "");
    });
  }, []);

  const choose = async () => {
    const selected = await pickFolder();
    if (!selected) return;
    setParent(selected);
    const identity = await getGitIdentity(selected).catch(() => null);
    if (identity?.name) setName(identity.name);
    if (identity?.email) setEmail(identity.email);
  };

  const run = async () => {
    setBusy(true);
    setError(null);
    try {
      await initializeProjectLocalRepository(
        state.projectId,
        parent,
        folder.trim(),
        message.trim(),
        name.trim(),
        email.trim(),
        key,
      );
      onDone();
    } catch (e) {
      setError(String(e));
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="handoff__section">
      <h3>Set up a local workspace</h3>
      <p className="muted">
        Create a clean local Git repository from the current project. Commitarium's internal agent
        history stays private.
      </p>
      {error && <div className="banner banner--error">{error}</div>}
      <div className="settings-row">
        <label>
          Parent folder
          <div className="row">
            <input value={parent} readOnly placeholder="Choose where to create the project" />
            <button type="button" onClick={() => void choose()} disabled={busy}>
              Choose…
            </button>
          </div>
        </label>
        <label>
          Folder name
          <input
            value={folder}
            onChange={(event) => setFolder(event.target.value)}
            disabled={busy}
          />
        </label>
      </div>
      <div className="settings-row">
        <label>
          Git author name
          <input value={name} onChange={(event) => setName(event.target.value)} disabled={busy} />
        </label>
        <label>
          Git author email
          <input value={email} onChange={(event) => setEmail(event.target.value)} disabled={busy} />
        </label>
      </div>
      <div className="settings-row">
        <label>
          Initial commit message
          <input
            value={message}
            onChange={(event) => setMessage(event.target.value)}
            disabled={busy}
          />
        </label>
      </div>
      <button
        className="primary"
        onClick={() => void run()}
        disabled={
          busy || !parent || !folder.trim() || !name.trim() || !email.trim() || !message.trim()
        }
      >
        {busy ? "Creating local repository…" : "Create local repository"}
      </button>
    </div>
  );
}

function LocalSync({ state, onDone }: { state: ProjectSyncState; onDone: () => void }) {
  const git = state.source?.sourceType === "git";
  const behind = state.local.unsyncedFeatures;
  const inSync = behind.length === 0;
  const [message, setMessage] = useState(
    `Sync from Commitarium (${state.canonical.headCommitId.slice(0, 12)})`,
  );
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [ok, setOk] = useState<string | null>(null);

  const run = async () => {
    setBusy(true);
    setError(null);
    setOk(null);
    try {
      const r = await synchronizeProjectLocally(
        state.projectId,
        message.trim() || "Sync from Commitarium",
      );
      setOk(
        r.created
          ? git
            ? `✓ Committed ${r.localCommitId?.slice(0, 12)} to ${r.sourcePath} (${r.targetBranch}).`
            : `✓ Wrote the project into ${r.sourcePath}.`
          : "Already up to date — nothing to sync.",
      );
      onDone();
    } catch (e) {
      setError(String(e));
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="handoff__section">
      <h3>{git ? "Local repository" : "Local folder"}</h3>
      {error && <div className="banner banner--error">{error}</div>}
      {inSync ? (
        <p className="muted note">
          ✓ Your {git ? "repository" : "folder"} is up to date with the project.
        </p>
      ) : (
        <>
          <p className="muted">
            {behind.length} work order{behind.length === 1 ? "" : "s"} not yet on your machine:
          </p>
          <ul className="sync__list">
            {behind.map((f) => (
              <li key={f.featureId}>{f.title}</li>
            ))}
          </ul>
          {git && (
            <div className="settings-row">
              <label>
                Commit message
                <input
                  value={message}
                  onChange={(e) => setMessage(e.target.value)}
                  disabled={busy}
                />
              </label>
            </div>
          )}
          <button className="primary" onClick={() => void run()} disabled={busy}>
            {busy ? "Syncing…" : git ? "Sync to local repo" : "Sync to folder"}
          </button>
        </>
      )}
      {ok && <p className="muted note">{ok}</p>}
    </div>
  );
}

function CreateRemote({
  state,
  projectName,
  onDone,
}: {
  state: ProjectSyncState;
  projectName: string;
  onDone: () => void;
}) {
  const draftKey = `commitarium.remoteSetup.${state.projectId}`;
  const [saved] = useState(() => loadRemoteDraft(draftKey));
  const [providers, setProviders] = useState<GitProviderProbe[] | null>(null);
  const [providerId, setProviderId] = useState(saved?.provider ?? "");
  const [namespace, setNamespace] = useState(saved?.namespace ?? "");
  const [repo, setRepo] = useState(saved?.repositoryName ?? slug(projectName));
  const [visibility, setVisibility] = useState<"private" | "public" | "internal">(
    saved?.visibility ?? "private",
  );
  const [azureProject, setAzureProject] = useState(saved?.azureProject ?? "");
  const [key] = useState(saved?.idempotencyKey ?? newIdempotencyKey);
  const [busy, setBusy] = useState(false);
  const [confirming, setConfirming] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [ok, setOk] = useState<string | null>(null);

  useEffect(() => {
    void probeGitProviders()
      .then((result) => {
        setProviders(result);
        const remembered = window.localStorage.getItem("commitarium.gitProvider");
        const ready = result.filter((item) => item.canCreate);
        const selected = saved
          ? ready.find((item) => item.provider === saved.provider)
          : (ready.find((item) => item.provider === remembered) ??
            ready.find((item) => item.provider === "github") ??
            ready[0]);
        if (selected) {
          setProviderId(selected.provider);
          setNamespace(
            (current) =>
              current || (selected.provider === "azure_devops" ? "" : selected.account || ""),
          );
        }
      })
      .catch((reason) => setError(String(reason)));
  }, [saved]);

  const provider = providers?.find((item) => item.provider === providerId);
  const chooseProvider = (value: string) => {
    setProviderId(value);
    const selected = providers?.find((item) => item.provider === value);
    setNamespace(selected?.provider === "azure_devops" ? "" : (selected?.account ?? ""));
    setVisibility("private");
    setConfirming(false);
  };

  const create = async () => {
    if (!provider) return;
    setBusy(true);
    setError(null);
    setOk(null);
    window.localStorage.setItem(
      draftKey,
      JSON.stringify({
        provider: provider.provider,
        namespace: namespace.trim(),
        repositoryName: repo.trim(),
        visibility,
        azureProject: provider.provider === "azure_devops" ? azureProject.trim() : undefined,
        idempotencyKey: key,
      } satisfies RemoteSetupDraft),
    );
    try {
      const result = await createProjectRemote({
        projectId: state.projectId,
        provider: provider.provider,
        namespace: namespace.trim(),
        repositoryName: repo.trim(),
        visibility,
        azureProject: provider.provider === "azure_devops" ? azureProject.trim() : undefined,
        idempotencyKey: key,
      });
      window.localStorage.setItem("commitarium.gitProvider", provider.provider);
      window.localStorage.removeItem(draftKey);
      setOk(`✓ Published ${result.defaultBranch} to ${result.webUrl}.`);
      setConfirming(false);
      onDone();
    } catch (e) {
      setError(String(e));
    } finally {
      setBusy(false);
    }
  };

  if (!providers) {
    return (
      <div className="handoff__section">
        <h3>Connect a remote repository</h3>
        <p className="muted note">Checking installed Git providers…</p>
      </div>
    );
  }

  const installed = providers.filter((item) => item.installed);
  return (
    <div className="handoff__section">
      <h3>
        Connect a remote repository <span className="muted">— optional</span>
      </h3>
      <p className="muted">
        Your local repository is ready. You can keep it local, or create an empty repository with
        one of your installed provider CLIs and push the current default branch.
      </p>
      {error && <div className="banner banner--error">{error}</div>}
      {ok && <p className="muted note">{ok}</p>}
      {installed.length === 0 ? (
        <>
          <p className="muted note">
            No supported provider CLI was found. The project will remain local.
          </p>
          <div className="row">
            {providers.map((item) => (
              <button
                key={item.provider}
                type="button"
                onClick={() => void openExternal(item.installUrl)}
              >
                Install {item.displayName} CLI
              </button>
            ))}
          </div>
        </>
      ) : (
        <>
          <div className="settings-row">
            <label>
              Provider
              <select
                value={providerId}
                onChange={(event) => chooseProvider(event.target.value)}
                disabled={busy}
              >
                <option value="">Local repository only</option>
                {installed.map((item) => (
                  <option key={item.provider} value={item.provider} disabled={!item.canCreate}>
                    {item.displayName}
                    {item.canCreate
                      ? item.account
                        ? ` — ${item.account}`
                        : " — ready"
                      : " — sign in required"}
                  </option>
                ))}
              </select>
            </label>
            {provider && (
              <label>
                {provider.provider === "azure_devops" ? "Organization URL" : "Owner / namespace"}
                <input
                  value={namespace}
                  onChange={(event) => setNamespace(event.target.value)}
                  disabled={busy}
                />
              </label>
            )}
          </div>
          {provider && (
            <>
              <div className="settings-row">
                <label>
                  Remote repository name
                  <input
                    value={repo}
                    onChange={(event) => setRepo(event.target.value)}
                    disabled={busy}
                  />
                </label>
                {provider.provider === "azure_devops" ? (
                  <label>
                    Azure DevOps project
                    <input
                      value={azureProject}
                      onChange={(event) => setAzureProject(event.target.value)}
                      disabled={busy}
                    />
                  </label>
                ) : (
                  <label>
                    Visibility
                    <select
                      value={visibility}
                      onChange={(event) => setVisibility(event.target.value as typeof visibility)}
                      disabled={busy}
                    >
                      <option value="private">Private</option>
                      <option value="public">Public</option>
                      {provider.provider === "gitlab" && <option value="internal">Internal</option>}
                    </select>
                  </label>
                )}
              </div>
              {!confirming ? (
                <button
                  className="primary"
                  onClick={() => setConfirming(true)}
                  disabled={
                    !provider.canCreate ||
                    !namespace.trim() ||
                    !repo.trim() ||
                    (provider.provider === "azure_devops" && !azureProject.trim())
                  }
                >
                  Review remote creation
                </button>
              ) : (
                <div className="banner banner--warn">
                  <p>
                    Create{" "}
                    <strong>
                      {namespace}/{repo}
                    </strong>{" "}
                    on {provider.displayName} and push the local{" "}
                    <strong>{state.canonical.defaultBranch}</strong> branch? This creates an
                    external repository.
                  </p>
                  <div className="row">
                    <button className="primary" onClick={() => void create()} disabled={busy}>
                      {busy ? "Creating and pushing…" : "Confirm create and push"}
                    </button>
                    <button type="button" onClick={() => setConfirming(false)} disabled={busy}>
                      Cancel
                    </button>
                  </div>
                </div>
              )}
            </>
          )}
          {installed
            .filter((item) => !item.canCreate)
            .map((item) => (
              <p className="muted note" key={item.provider}>
                {item.displayName}: {item.detail} Sign in from a terminal, then reopen this panel.
              </p>
            ))}
        </>
      )}
    </div>
  );
}

function Push({ state, onDone }: { state: ProjectSyncState; onDone: () => void }) {
  const [preview, setPreview] = useState<ProjectUpstreamResult | null>(null);
  const [remote, setRemote] = useState("");
  const [branch, setBranch] = useState("");
  const [busy, setBusy] = useState<null | "preview" | "push">(null);
  const [error, setError] = useState<string | null>(null);

  // Only meaningful once the local repo holds the current canonical head.
  const localSynced = state.local.watermarkCommitId === state.canonical.headCommitId;

  const doPreview = async () => {
    setBusy("preview");
    setError(null);
    try {
      const p = await previewProjectUpstreamBranch(state.projectId);
      setPreview(p);
      setRemote(p.selectedRemote ?? p.remotes[0]?.name ?? "");
      setBranch(p.branchName);
    } catch (e) {
      setError(String(e));
    } finally {
      setBusy(null);
    }
  };

  const doPush = async () => {
    if (!remote || !branch.trim()) return;
    setBusy("push");
    setError(null);
    try {
      setPreview(await publishProjectUpstreamBranch(state.projectId, remote, branch.trim()));
      onDone();
    } catch (e) {
      setError(String(e));
    } finally {
      setBusy(null);
    }
  };

  const done = preview?.status === "published" || preview?.status === "already_published";

  return (
    <div className="handoff__section">
      <h3>Push to a remote branch</h3>
      <p className="muted">
        Push the current project sync to a new branch on your git remote. Never force-pushes or
        touches an existing branch.
      </p>
      {error && <div className="banner banner--error">{error}</div>}
      {!localSynced ? (
        <p className="muted note">Sync to your local repo first; the push mirrors that commit.</p>
      ) : !preview ? (
        <button className="primary" onClick={() => void doPreview()} disabled={busy != null}>
          {busy === "preview" ? "Checking remotes…" : "Prepare push"}
        </button>
      ) : done ? (
        <p className="muted note">
          {preview.status === "published" ? "✓ Pushed to " : "Already on "}
          {remote}/{preview.branchName}.
        </p>
      ) : (
        <>
          {upstreamHint(preview.status, preview.detail) && (
            <p className="muted note">{upstreamHint(preview.status, preview.detail)}</p>
          )}
          <div className="settings-row">
            <label>
              Remote
              <select
                value={remote}
                onChange={(e) => setRemote(e.target.value)}
                disabled={busy != null}
              >
                {preview.remotes.map((r) => (
                  <option key={r.name} value={r.name}>
                    {r.name} — {r.displayLocation}
                  </option>
                ))}
              </select>
            </label>
            <label>
              Branch
              <input
                value={branch}
                onChange={(e) => setBranch(e.target.value)}
                disabled={busy != null}
              />
            </label>
          </div>
          <button
            className="primary"
            onClick={() => void doPush()}
            disabled={busy != null || !remote || !branch.trim()}
          >
            {busy === "push" ? "Pushing…" : "Push branch"}
          </button>
        </>
      )}
    </div>
  );
}

function slug(value: string): string {
  return (
    value
      .trim()
      .toLowerCase()
      .replace(/[^a-z0-9._-]+/g, "-")
      .replace(/^-+|-+$/g, "") || "project"
  );
}

function newIdempotencyKey(): string {
  return (
    globalThis.crypto?.randomUUID?.() ??
    `commitarium-${Date.now()}-${Math.random().toString(16).slice(2)}`
  );
}

interface RemoteSetupDraft {
  provider: GitProviderProbe["provider"];
  namespace: string;
  repositoryName: string;
  visibility: "private" | "public" | "internal";
  azureProject?: string;
  idempotencyKey: string;
}

function loadRemoteDraft(key: string): RemoteSetupDraft | null {
  try {
    const value = window.localStorage.getItem(key);
    return value ? (JSON.parse(value) as RemoteSetupDraft) : null;
  } catch {
    return null;
  }
}

function upstreamHint(
  status: ProjectUpstreamResult["status"],
  detail: string | null,
): string | null {
  switch (status) {
    case "selection_required":
      return "Choose which remote to push to.";
    case "branch_conflict":
      return detail || "That branch already exists with different content — pick another name.";
    case "authentication_required":
      return "Git needs credentials — set up your credential helper or SSH key, then retry.";
    case "remote_unavailable":
      return detail || "Couldn't reach the remote.";
    default:
      return null;
  }
}
