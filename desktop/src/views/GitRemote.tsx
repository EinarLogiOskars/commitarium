import { useCallback, useEffect, useState } from "react";
import {
  getLinkedRepository,
  linkRepository,
  pullFromLinked,
  pushToLinked,
  unlinkRepository,
  type LinkedRepository,
} from "../api/linkedRepository";
import { sendToAssistant } from "../api/assistant";
import { openExternal } from "../ipc";

// A project's linked repository on GitHub or another git host
// (SERVER_COMPLETE_PLAN.md, S2). Both ways are the user's to start: Pull
// brings commits in, and Mayka merges them when main has its own; Push
// sends main to a branch the user names there, for a pull request.
export function GitRemote({ projectId, compact }: { projectId: string; compact?: boolean }) {
  const [linked, setLinked] = useState<LinkedRepository | null | undefined>(undefined);
  const [busy, setBusy] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [note, setNote] = useState<string | null>(null);
  const [pullRequest, setPullRequest] = useState<string | null>(null);
  // The branch being named, while the push form is open.
  const [pushBranch, setPushBranch] = useState<string | null>(null);

  const load = useCallback(
    () =>
      void getLinkedRepository(projectId)
        .then(setLinked)
        .catch((e) => setError(String(e))),
    [projectId],
  );
  useEffect(load, [load]);

  const act = async (what: string, work: () => Promise<string | void>) => {
    setBusy(what);
    setError(null);
    setNote(null);
    setPullRequest(null);
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
  if (linked === null) return <LinkForm projectId={projectId} compact={compact} onLinked={load} />;

  const name = linked.url.replace(/^https:\/\//, "").replace(/\.git$/, "");
  const commits = (n: number) => `${n} commit${n === 1 ? "" : "s"}`;
  const pull = () =>
    act("pull", async () => {
      const pulled = await pullFromLinked(projectId);
      if (pulled.outcome === "up_to_date") return "Nothing new there.";
      if (pulled.outcome === "pulled") return `Pulled ${commits(pulled.commits)} into main.`;
      // Main has commits of its own: Mayka merges the two.
      await sendToAssistant(
        projectId,
        `I pulled ${commits(pulled.commits)} from the linked repository ${linked.url}, ` +
          `but main has commits of its own, so they are on the branch ${pulled.local_branch}. ` +
          `Please open a work order that merges ${pulled.local_branch} into main, ` +
          `keeping the work from both sides.`,
        crypto.randomUUID(),
      );
      return `Main has commits of its own, so Mayka is merging the ${commits(pulled.commits)} in a work order.`;
    });
  const push = (branch: string) =>
    act("push", async () => {
      const pushed = await pushToLinked(projectId, branch);
      setPushBranch(null);
      if (pushed.commits === 0) return "Nothing on main the repository lacks.";
      if (pushed.pull_request_url) setPullRequest(pushed.pull_request_url);
      return (
        `Pushed ${commits(pushed.commits)} to ${pushed.branch} there. ` +
        `It is a branch of its own: merge it into ${linked.branch} there to finish.`
      );
    });

  return (
    <div className="shell-card__text">
      <button
        className="shell-card__link"
        onClick={() => void openExternal(linked.url.replace(/\.git$/, ""))}
      >
        {name} ↗
      </button>
      {!compact && (
        <p className="muted">
          Pull brings the commits made there into main; when main has its own, Mayka merges the two
          in a work order. Push sends main to a branch you name there, for a pull request into{" "}
          {linked.branch}. Nothing is forced, and nothing syncs on its own.
        </p>
      )}
      <div className="row deploy-links">
        <button className="shell-card__link" disabled={busy !== null} onClick={() => void pull()}>
          {busy === "pull" ? "Pulling…" : "Pull"}
        </button>
        <button
          className="shell-card__link"
          disabled={busy !== null || !linked.has_token}
          title={linked.has_token ? undefined : "Pushing needs an access token"}
          onClick={() => setPushBranch(pushBranch === null ? linked.push_branch : null)}
        >
          Push
        </button>
        {pullRequest && (
          <button className="shell-card__link" onClick={() => void openExternal(pullRequest)}>
            Open a pull request ↗
          </button>
        )}
      </div>
      {pushBranch !== null && (
        <div className="deploy-address">
          <label className="muted">
            Branch there
            <input
              value={pushBranch}
              autoCapitalize="off"
              autoCorrect="off"
              spellCheck={false}
              onChange={(e) => setPushBranch(e.target.value)}
            />
          </label>
          <p className="muted">
            Main goes to this branch, not to {linked.branch}. Remember to merge it into{" "}
            {linked.branch} there, through a pull request, to get it all the way.
          </p>
          <button
            disabled={busy !== null || !pushBranch.trim() || pushBranch.trim() === linked.branch}
            onClick={() => void push(pushBranch.trim())}
          >
            {busy === "push" ? "Pushing…" : `Push to ${pushBranch.trim() || "…"}`}
          </button>
        </div>
      )}
      {note && <p className="muted">{note}</p>}
      {error && <p className="sync-card__error">{error}</p>}
      {!compact && (
        <div className="deploy-address">
          {(linked.pulled_at || linked.pushed_at) && (
            <p className="muted">
              {linked.pulled_at && `Last pulled ${new Date(linked.pulled_at).toLocaleString()}. `}
              {linked.pushed_at && `Last pushed ${new Date(linked.pushed_at).toLocaleString()}.`}
            </p>
          )}
          {!linked.has_token && (
            <p className="muted">Pushing needs an access token. Link again with one to push.</p>
          )}
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
          ? "Link a repository on GitHub or another git host, to pull from and push to."
          : "Link this project to a repository on GitHub or another git host, to pull the commits made there and push main back, so the code also lives off this server. Give an access token that can read and push; a public repository can be linked without one, but then only pulls."}
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
