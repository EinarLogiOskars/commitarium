import { useCallback, useEffect, useRef, useState } from "react";
import {
  listProfiles,
  beginLogin,
  submitLoginCode,
  submitApiKey,
  cancelLogin,
  disconnectProfile,
  onLoginProgress,
  openExternal,
  stackUp,
  type Profile,
  type ProfileStatus,
} from "../ipc";
import { createAgent, deleteAgent, renameAgent } from "../api/agents";
import { ApiError } from "../api/client";
import type { AgentProvider } from "../api/types";
import { reloadAgents } from "./useAgents";

const PROVIDER_LABEL: Record<AgentProvider, string> = { codex: "Codex", claude: "Claude" };

const STATUS_LABEL: Record<ProfileStatus, string> = {
  not_configured: "Not connected",
  starting: "Starting…",
  waiting_for_browser: "Waiting for browser…",
  waiting_for_code: "Waiting for code…",
  waiting_for_api_key: "Enter API key",
  verifying: "Verifying…",
  connected: "Connected",
  expired: "Expired",
  failed: "Failed",
};

function tone(s: ProfileStatus): "ok" | "bad" | "warn" | "muted" {
  if (s === "connected") return "ok";
  if (s === "failed" || s === "expired") return "bad";
  if (s === "not_configured") return "muted";
  return "warn";
}

/** Agents (ADR-016): one per provider account. Each signs in once and can lead
 * or review any number of work orders. */
export function Agents({ onClose }: { onClose: () => void }) {
  const [profiles, setProfiles] = useState<Profile[] | null>(null);
  const [error, setError] = useState<string | null>(null);

  const refresh = useCallback(async () => {
    try {
      setProfiles(await listProfiles());
    } catch (e) {
      setError(String(e));
    }
  }, []);

  // Agent changes also refresh the pickers in other views.
  const changed = useCallback(async () => {
    await reloadAgents().catch(() => undefined);
    await refresh();
  }, [refresh]);

  useEffect(() => {
    void refresh();
    const unlisten = onLoginProgress((p) => {
      setProfiles((prev) =>
        (prev ?? []).map((x) =>
          x.id === p.profileId ? { ...x, status: p.status, detail: p.detail } : x,
        ),
      );
      // A newly connected agent's worker starts with the rest of the stack.
      if (p.status === "connected") void stackUp().catch((e) => setError(String(e)));
    });
    return () => {
      void unlisten.then((fn) => fn());
    };
  }, [refresh]);

  return (
    <div className="modal" onClick={onClose}>
      <div className="modal__card" onClick={(e) => e.stopPropagation()}>
        <h2>Agents</h2>
        <p className="muted">
          Connect each agent to its Codex or Claude account. An agent signs in once and serves as
          lead or reviewer; secrets go straight to a private volume and never touch the coordinator.
        </p>
        {error && <div className="banner banner--error">{error}</div>}

        <div className="profiles">
          {profiles === null ? (
            <p className="muted">Loading…</p>
          ) : profiles.length === 0 ? (
            <p className="muted">No agents yet. Add one for each account you want to use.</p>
          ) : (
            profiles.map((p) => (
              <ProfileRow key={p.id} profile={p} onChanged={refresh} onAgentsChanged={changed} />
            ))
          )}
        </div>

        <AddAgent onAdded={changed} />

        <div className="modal__footer">
          <button className="ghost" onClick={onClose}>
            Close
          </button>
        </div>
      </div>
    </div>
  );
}

function AddAgent({ onAdded }: { onAdded: () => Promise<void> }) {
  const [name, setName] = useState("");
  const [provider, setProvider] = useState<AgentProvider>("claude");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const add = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!name.trim()) return;
    setBusy(true);
    setError(null);
    try {
      await createAgent(name.trim(), provider);
      setName("");
      await onAdded();
    } catch (e) {
      setError(describe(e));
    } finally {
      setBusy(false);
    }
  };

  return (
    <form className="agent-add" onSubmit={add}>
      {error && <div className="banner banner--error">{error}</div>}
      <div className="row">
        <input
          type="text"
          placeholder="Name, e.g. Claude Max"
          value={name}
          onChange={(e) => setName(e.target.value)}
          disabled={busy}
        />
        <select
          value={provider}
          onChange={(e) => setProvider(e.target.value as AgentProvider)}
          disabled={busy}
        >
          <option value="claude">Claude</option>
          <option value="codex">Codex</option>
        </select>
        <button className="primary" type="submit" disabled={busy || !name.trim()}>
          Add agent
        </button>
      </div>
    </form>
  );
}

function describe(e: unknown): string {
  if (e instanceof ApiError && e.code === "agent_in_use") {
    return "This agent is still chosen by a project or a work order that has not finished.";
  }
  return e instanceof ApiError ? e.message : String(e);
}

function ProfileRow({
  profile,
  onChanged,
  onAgentsChanged,
}: {
  profile: Profile;
  onChanged: () => void;
  onAgentsChanged: () => Promise<void>;
}) {
  const [renaming, setRenaming] = useState<string | null>(null);
  const [picking, setPicking] = useState(false);
  const [code, setCode] = useState("");
  const [apiKey, setApiKey] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const openedUrl = useRef<string | null>(null);

  const s = profile.status;
  const active = [
    "starting",
    "waiting_for_browser",
    "waiting_for_code",
    "waiting_for_api_key",
    "verifying",
  ].includes(s);

  // Auto-open the provider login page once when it appears.
  useEffect(() => {
    const url = profile.detail?.browserUrl;
    if (url && openedUrl.current !== url) {
      openedUrl.current = url;
      void openExternal(url);
    }
  }, [profile.detail?.browserUrl]);

  const run = async (fn: () => Promise<unknown>) => {
    setBusy(true);
    setError(null);
    try {
      await fn();
      onChanged();
    } catch (e) {
      setError(describe(e));
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="profile-row">
      <div className="profile-row__head">
        {renaming === null ? (
          <span className="profile-row__name">
            {profile.name} <span className="muted">· {PROVIDER_LABEL[profile.provider]}</span>
          </span>
        ) : (
          <input
            type="text"
            value={renaming}
            onChange={(e) => setRenaming(e.target.value)}
            disabled={busy}
            autoFocus
          />
        )}
        <span className={`state state--${tone(s)}`}>
          <span className={`dot dot--${tone(s)}`} />
          {STATUS_LABEL[s]}
        </span>
      </div>

      {profile.detail?.message && (
        <p className="muted profile-row__msg">{profile.detail.message}</p>
      )}
      {error && <div className="banner banner--error">{error}</div>}

      {renaming !== null && (
        <div className="row">
          <button
            className="primary"
            onClick={() =>
              void run(async () => {
                await renameAgent(profile.id, renaming.trim());
                setRenaming(null);
                await onAgentsChanged();
              })
            }
            disabled={busy || !renaming.trim()}
          >
            Save name
          </button>
          <button className="ghost" onClick={() => setRenaming(null)} disabled={busy}>
            Cancel
          </button>
        </div>
      )}

      {/* Idle: connect / disconnect */}
      {!active && s === "connected" && renaming === null && (
        <div className="row">
          <button onClick={() => void run(() => disconnectProfile(profile.id))} disabled={busy}>
            Disconnect
          </button>
          <button className="ghost" onClick={() => setRenaming(profile.name)} disabled={busy}>
            Rename
          </button>
        </div>
      )}
      {!active && s !== "connected" && !picking && renaming === null && (
        <div className="row">
          <button className="primary" onClick={() => setPicking(true)} disabled={busy}>
            Connect
          </button>
          <button className="ghost" onClick={() => setRenaming(profile.name)} disabled={busy}>
            Rename
          </button>
          <button
            className="ghost"
            onClick={() => {
              if (
                !window.confirm(
                  `Remove ${profile.name}? Projects and work orders can no longer use it.`,
                )
              )
                return;
              void run(async () => {
                await deleteAgent(profile.id);
                await onAgentsChanged();
              });
            }}
            disabled={busy}
          >
            Remove
          </button>
        </div>
      )}
      {!active && s !== "connected" && picking && (
        <div className="row">
          <button
            className="primary"
            onClick={() =>
              run(() => beginLogin(profile.id, "subscription")).then(() => setPicking(false))
            }
            disabled={busy}
          >
            Subscription
          </button>
          <button
            onClick={() =>
              run(() => beginLogin(profile.id, "api_key")).then(() => setPicking(false))
            }
            disabled={busy}
          >
            API key
          </button>
          <button className="ghost" onClick={() => setPicking(false)} disabled={busy}>
            Cancel
          </button>
        </div>
      )}

      {/* Active login states */}
      {profile.detail?.deviceCode && (
        <p className="profile-row__code">
          Enter this code on the page: <span className="mono">{profile.detail.deviceCode}</span>
        </p>
      )}
      {profile.detail?.browserUrl && (
        <div className="row">
          <button onClick={() => void openExternal(profile.detail!.browserUrl!)}>
            Open login page
          </button>
        </div>
      )}

      {profile.provider === "claude" &&
        ["starting", "waiting_for_browser", "waiting_for_code"].includes(s) && (
          <div className="row">
            <input
              type="text"
              placeholder="Paste the code from the browser"
              value={code}
              onChange={(e) => setCode(e.target.value)}
              disabled={busy}
            />
            <button
              className="primary"
              onClick={() =>
                run(() => submitLoginCode(profile.id, code.trim())).then(() => setCode(""))
              }
              disabled={busy || !code.trim()}
            >
              Submit code
            </button>
          </div>
        )}

      {s === "waiting_for_api_key" && (
        <div className="api-key">
          <input
            type="password"
            placeholder="Paste your API key"
            value={apiKey}
            onChange={(e) => setApiKey(e.target.value)}
            disabled={busy}
          />
          <button
            className="primary"
            onClick={() =>
              run(() => submitApiKey(profile.id, apiKey.trim())).then(() => setApiKey(""))
            }
            disabled={busy || !apiKey.trim()}
          >
            Save key
          </button>
        </div>
      )}

      {active && (
        <div className="row">
          <button
            className="ghost"
            onClick={() => void run(() => cancelLogin(profile.id))}
            disabled={busy}
          >
            Cancel login
          </button>
        </div>
      )}
    </div>
  );
}
