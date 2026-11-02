import { useCallback, useEffect, useState } from "react";
import {
  addMirror,
  listMirrors,
  pushMirrorsNow,
  removeMirror,
  type PushMirror,
} from "../api/mirrors";
import { openExternal } from "../ipc";

const POLL_MS = 15000;

// A project's copy on a git remote such as GitHub (SERVER_COMPLETE_PLAN.md,
// S2): Forgejo pushes to it on every merge, so on a server the code also
// lives somewhere else.
export function GitRemote({ projectId, compact }: { projectId: string; compact?: boolean }) {
  const [mirrors, setMirrors] = useState<PushMirror[] | null>(null);
  const [address, setAddress] = useState("");
  const [token, setToken] = useState("");
  const [username, setUsername] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const load = useCallback(
    () =>
      void listMirrors(projectId)
        .then((found) => setMirrors(found.mirrors ?? []))
        .catch((e) => setError(String(e))),
    [projectId],
  );
  useEffect(() => {
    load();
    const timer = setInterval(load, POLL_MS);
    return () => clearInterval(timer);
  }, [load]);

  const act = async (work: () => Promise<unknown>) => {
    setBusy(true);
    setError(null);
    try {
      await work();
      load();
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    } finally {
      setBusy(false);
    }
  };

  if (!mirrors) return <p className="muted shell-card__text">Checking…</p>;
  return (
    <>
      {mirrors.map((mirror) => (
        <div key={mirror.name} className="shell-card__text">
          <button
            className="shell-card__link"
            onClick={() => void openExternal(mirror.remote_url.replace(/\.git$/, ""))}
          >
            {mirror.remote_url.replace(/^https:\/\//, "").replace(/\.git$/, "")} ↗
          </button>
          <p className="muted">
            {mirror.last_error
              ? `The last push failed: ${mirror.last_error}`
              : pushedAt(mirror.last_update)
                ? `Pushed ${pushedAt(mirror.last_update)}`
                : "Pushing for the first time…"}
          </p>
          <div className="row deploy-links">
            <button
              className="shell-card__link"
              disabled={busy}
              onClick={() => void act(() => pushMirrorsNow(projectId))}
            >
              Push now
            </button>
            <button
              className="shell-card__link"
              disabled={busy}
              onClick={() => void act(() => removeMirror(projectId, mirror.name))}
              title="Stops pushing; the remote keeps its copy"
            >
              Remove
            </button>
          </div>
        </div>
      ))}
      {mirrors.length === 0 && (
        <div className="deploy-address">
          <p className="muted shell-card__text">
            {compact
              ? "Keep a copy of the code on GitHub or another git host."
              : "Keep a copy of this project's code on GitHub or another git host. Commitarium pushes to it after every merge, so the code also lives off this server. Create an empty repository there, and an access token that can push to it."}
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
          <button
            disabled={busy || !address.trim() || !token.trim()}
            onClick={() =>
              void act(async () => {
                await addMirror(projectId, address.trim(), token.trim(), username.trim());
                setToken("");
              })
            }
          >
            {busy ? "Setting up…" : "Keep a copy there"}
          </button>
        </div>
      )}
      {error && <p className="sync-card__error">{error}</p>}
    </>
  );
}

/** When it last pushed, or null for never: Forgejo says never with a date
 * long past. */
function pushedAt(when?: string): string | null {
  if (!when) return null;
  const date = new Date(when);
  return date.getFullYear() < 2000 ? null : date.toLocaleString();
}
