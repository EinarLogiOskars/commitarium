import { useCallback, useEffect, useState } from "react";
import {
  bringInFromLinked,
  getLinkedRepository,
  linkRepository,
  pushToLinked,
  setSyncAutomatic,
  unlinkRepository,
  type LinkedRepository,
} from "../api/linkedRepository";
import { sendToAssistant } from "../api/assistant";
import { openExternal } from "../ipc";

const POLL_MS = 30000;

// A project's linked repository on GitHub or another git host
// (SERVER_COMPLETE_PLAN.md, S2): its commits come in, main's go out, never
// forced. When both sides moved, Mayka merges them in a work order.
export function GitRemote({ projectId, compact }: { projectId: string; compact?: boolean }) {
  const [linked, setLinked] = useState<LinkedRepository | null | undefined>(undefined);
  const [busy, setBusy] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [note, setNote] = useState<string | null>(null);

  const load = useCallback(
    () =>
      void getLinkedRepository(projectId)
        .then(setLinked)
        .catch((e) => setError(String(e))),
    [projectId],
  );
  useEffect(() => {
    load();
    const timer = setInterval(load, POLL_MS);
    return () => clearInterval(timer);
  }, [load]);

  const act = async (what: string, work: () => Promise<string | void>) => {
    setBusy(what);
    setError(null);
    setNote(null);
    try {
      const said = await work();
      if (said) setNote(said);
      load();
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    } finally {
      setBusy(null);
    }
  };

  if (linked === undefined) return <p className="muted shell-card__text">Checking…</p>;
  if (linked === null)
    return <LinkForm projectId={projectId} compact={compact} onLinked={load} />;

  const comparison = linked.comparison;
  const name = linked.url.replace(/^https:\/\//, "").replace(/\.git$/, "");
  const askMayka = () =>
    act("mayka", async () => {
      await sendToAssistant(
        projectId,
        `The linked repository ${linked.url} and this project's main both have new commits. ` +
          `Its commits are on the branch ${linked.local_branch} here. ` +
          `Please open a work order that merges ${linked.local_branch} into main, ` +
          `keeping the work from both sides.`,
        crypto.randomUUID(),
      );
      return "Mayka is merging them. Push once that work order merges.";
    });

  return (
    <div className="shell-card__text">
      <button className="shell-card__link" onClick={() => void openExternal(linked.url.replace(/\.git$/, ""))}>
        {name} ↗
      </button>
      <p className="muted">{standing(linked)}</p>
      {linked.last_error && <p className="sync-card__error">{linked.last_error}</p>}
      <div className="row deploy-links">
        {(comparison?.state === "theirs_ahead" || comparison?.state === "diverged") && (
          <button
            className="shell-card__link"
            disabled={busy !== null}
            onClick={() =>
              void act("bring-in", async () => {
                const after = await bringInFromLinked(projectId);
                return after.state === "diverged"
                  ? `Main has commits of its own, so theirs wait on ${linked.local_branch}.`
                  : "Brought in.";
              })
            }
          >
            {busy === "bring-in" ? "Bringing in…" : "Bring in"}
          </button>
        )}
        {comparison?.state === "diverged" && (
          <button className="shell-card__link" disabled={busy !== null} onClick={() => void askMayka()}>
            Ask Mayka to merge them
          </button>
        )}
        {comparison?.state === "ours_ahead" && linked.has_token && (
          <button
            className="shell-card__link"
            disabled={busy !== null}
            onClick={() =>
              void act("push", async () => {
                await pushToLinked(projectId);
                return "Pushed.";
              })
            }
          >
            {busy === "push" ? "Pushing…" : "Push"}
          </button>
        )}
      </div>
      {!compact && (
        <div className="deploy-address">
          <label className="row muted">
            <input
              type="checkbox"
              checked={linked.automatic}
              disabled={busy !== null}
              onChange={(e) => {
                const automatic = e.target.checked;
                void act("automatic", () => setSyncAutomatic(projectId, automatic));
              }}
            />
            Keep in step on its own: every few minutes, bring their commits in and push main's
          </label>
          {!linked.has_token && (
            <p className="muted">
              Pushing needs an access token. Link again with one to push from here.
            </p>
          )}
          <p className="muted">
            Nothing is ever forced. When both sides have new commits, Mayka merges them in a work
            order, and that merge goes out like any other.
          </p>
          <button
            className="shell-card__link"
            disabled={busy !== null}
            title="Both repositories keep their code"
            onClick={() => void act("unlink", () => unlinkRepository(projectId))}
          >
            Unlink
          </button>
        </div>
      )}
      {note && <p className="muted">{note}</p>}
      {error && <p className="sync-card__error">{error}</p>}
    </div>
  );
}

function LinkForm({
  projectId,
  compact,
  onLinked,
}: {
  projectId: string;
  compact?: boolean;
  onLinked: () => void;
}) {
  const [address, setAddress] = useState("");
  const [token, setToken] = useState("");
  const [username, setUsername] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const link = async () => {
    setBusy(true);
    setError(null);
    try {
      await linkRepository(projectId, address.trim(), token.trim(), username.trim());
      setToken("");
      onLinked();
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="deploy-address">
      <p className="muted shell-card__text">
        {compact
          ? "Link a repository on GitHub or another git host, to sync both ways."
          : "Link this project to a repository on GitHub or another git host. Commits made there come in, and main goes out, so the code also lives off this server. Give an access token that can read and push; a public repository can be linked without one, but then only brings commits in."}
      </p>
      <label className="muted">
        Repository address
        <input
          placeholder="https://github.com/you/app.git"
          value={address}
          autoCapitalize="off"
          autoCorrect="off"
          spellCheck={false}
          onChange={(e) => setAddress(e.target.value)}
        />
      </label>
      <label className="muted">
        Access token
        <input
          type="password"
          value={token}
          autoCapitalize="off"
          autoCorrect="off"
          spellCheck={false}
          onChange={(e) => setToken(e.target.value)}
        />
      </label>
      {!compact && (
        <label className="muted">
          Username, if the host needs one
          <input
            value={username}
            autoCapitalize="off"
            autoCorrect="off"
            spellCheck={false}
            onChange={(e) => setUsername(e.target.value)}
          />
        </label>
      )}
      <button disabled={busy || !address.trim()} onClick={() => void link()}>
        {busy ? "Linking…" : "Link"}
      </button>
      {error && <p className="sync-card__error">{error}</p>}
    </div>
  );
}

/** How the two sides stand, in a line. */
function standing(linked: LinkedRepository): string {
  const c = linked.comparison;
  if (!c) return linked.compare_error ? `Could not compare: ${linked.compare_error}` : "Comparing…";
  const commits = (n: number) => `${n} commit${n === 1 ? "" : "s"}`;
  switch (c.state) {
    case "same":
      return `In step on ${linked.branch}.`;
    case "theirs_ahead":
      return `${commits(c.theirs)} there to bring in.`;
    case "ours_ahead":
      return linked.has_token
        ? `${commits(c.ours)} on main to push.`
        : `${commits(c.ours)} on main; pushing needs a token.`;
    case "diverged":
      return `Both moved: ${commits(c.theirs)} there, ${commits(c.ours)} here.`;
  }
}
