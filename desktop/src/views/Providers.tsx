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
  type Profile,
  type ProfileId,
  type ProfileStatus,
} from "../ipc";

const LABEL: Record<ProfileId, string> = {
  "codex-lead": "Codex · Lead",
  "codex-reviewer": "Codex · Reviewer",
  "claude-lead": "Claude · Lead",
  "claude-reviewer": "Claude · Reviewer",
};

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

export function Providers({ onClose }: { onClose: () => void }) {
  const [profiles, setProfiles] = useState<Profile[]>([]);
  const [error, setError] = useState<string | null>(null);

  const refresh = useCallback(async () => {
    try {
      setProfiles(await listProfiles());
    } catch (e) {
      setError(String(e));
    }
  }, []);

  useEffect(() => {
    void refresh();
    const unlisten = onLoginProgress((p) => {
      setProfiles((prev) =>
        prev.map((x) => (x.id === p.profileId ? { ...x, status: p.status, detail: p.detail } : x)),
      );
    });
    return () => {
      void unlisten.then((fn) => fn());
    };
  }, [refresh]);

  return (
    <div className="modal" onClick={onClose}>
      <div className="modal__card" onClick={(e) => e.stopPropagation()}>
        <h2>Providers</h2>
        <p className="muted">
          Connect the agents to your Codex and Claude accounts. Each role signs in on its
          own; secrets go straight to a private volume and never touch the coordinator.
        </p>
        {error && <div className="banner banner--error">{error}</div>}

        <div className="profiles">
          {profiles.length === 0 ? (
            <p className="muted">Loading…</p>
          ) : (
            profiles.map((p) => <ProfileRow key={p.id} profile={p} onChanged={refresh} />)
          )}
        </div>

        <div className="modal__footer">
          <button className="ghost" onClick={onClose}>Close</button>
        </div>
      </div>
    </div>
  );
}

function ProfileRow({ profile, onChanged }: { profile: Profile; onChanged: () => void }) {
  const [picking, setPicking] = useState(false);
  const [code, setCode] = useState("");
  const [apiKey, setApiKey] = useState("");
  const [useBoth, setUseBoth] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const openedUrl = useRef<string | null>(null);

  const s = profile.status;
  const active = ["starting", "waiting_for_browser", "waiting_for_code", "waiting_for_api_key", "verifying"].includes(s);

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
      setError(String(e));
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="profile-row">
      <div className="profile-row__head">
        <span className="profile-row__name">{LABEL[profile.id]}</span>
        <span className={`state state--${tone(s)}`}>
          <span className={`dot dot--${tone(s)}`} />
          {STATUS_LABEL[s]}
        </span>
      </div>

      {profile.detail?.message && <p className="muted profile-row__msg">{profile.detail.message}</p>}
      {error && <div className="banner banner--error">{error}</div>}

      {/* Idle: connect / disconnect */}
      {!active && s === "connected" && (
        <div className="row">
          <button onClick={() => void run(() => disconnectProfile(profile.id))} disabled={busy}>
            Disconnect
          </button>
        </div>
      )}
      {!active && s !== "connected" && !picking && (
        <div className="row">
          <button className="primary" onClick={() => setPicking(true)} disabled={busy}>
            Connect
          </button>
        </div>
      )}
      {!active && s !== "connected" && picking && (
        <div className="row">
          <button
            className="primary"
            onClick={() => run(() => beginLogin(profile.id, "subscription")).then(() => setPicking(false))}
            disabled={busy}
          >
            Subscription
          </button>
          <button
            onClick={() => run(() => beginLogin(profile.id, "api_key")).then(() => setPicking(false))}
            disabled={busy}
          >
            API key
          </button>
          <button className="ghost" onClick={() => setPicking(false)} disabled={busy}>Cancel</button>
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
          <button onClick={() => void openExternal(profile.detail!.browserUrl!)}>Open login page</button>
        </div>
      )}

      {s === "waiting_for_code" && profile.provider === "claude" && (
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
            onClick={() => run(() => submitLoginCode(profile.id, code.trim())).then(() => setCode(""))}
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
          <label className="api-key__both">
            <input type="checkbox" checked={useBoth} onChange={(e) => setUseBoth(e.target.checked)} disabled={busy} />
            Use this key for both {profile.provider} roles
          </label>
          <button
            className="primary"
            onClick={() => run(() => submitApiKey(profile.id, apiKey.trim(), useBoth)).then(() => setApiKey(""))}
            disabled={busy || !apiKey.trim()}
          >
            Save key
          </button>
        </div>
      )}

      {active && (
        <div className="row">
          <button className="ghost" onClick={() => void run(() => cancelLogin(profile.id))} disabled={busy}>
            Cancel login
          </button>
        </div>
      )}
    </div>
  );
}
