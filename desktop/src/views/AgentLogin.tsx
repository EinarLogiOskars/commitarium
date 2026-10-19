import { useCallback, useEffect, useState } from "react";
import { beginLogin, listProfiles, onLoginProgress, type Profile } from "../ipc";
import { afterAgentConnected, LoginSteps } from "./Agents";

/** Signs one agent back in where its work paused (ADR-017, ADR-034). Sign in
 * starts the provider's login right away; once the agent is connected, the
 * work waiting on it continues by itself. */
export function AgentLogin({
  agentId,
  agentName,
  continuing = "the work order is continuing",
}: {
  agentId: string;
  agentName: string;
  /** What goes on once the agent is connected, after "<agent> is connected; ". */
  continuing?: string;
}) {
  const [profile, setProfile] = useState<Profile | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const refresh = useCallback(async () => {
    try {
      setProfile((await listProfiles()).find((p) => p.id === agentId) ?? null);
    } catch (e) {
      setError(String(e));
    }
  }, [agentId]);

  useEffect(() => {
    const unlisten = onLoginProgress((p) => {
      if (p.profileId !== agentId) return;
      setProfile((prev) => (prev ? { ...prev, status: p.status, detail: p.detail } : prev));
      if (p.status === "connected")
        void afterAgentConnected(agentId).catch((e) => setError(String(e)));
    });
    return () => {
      void unlisten.then((fn) => fn());
    };
  }, [agentId]);

  const start = async (method: "subscription" | "api_key") => {
    setBusy(true);
    setError(null);
    try {
      if (!profile) await refresh();
      await beginLogin(agentId, method);
    } catch (e) {
      setError(String(e));
    } finally {
      setBusy(false);
    }
  };

  const status = profile?.status;
  const loggingIn =
    status != null && !["not_configured", "connected", "expired", "failed"].includes(status);
  return (
    <div className="agent-login">
      {error && <div className="banner banner--error">{error}</div>}
      {status === "connected" ? (
        <p className="muted">
          {agentName} is connected; {continuing}.
        </p>
      ) : !loggingIn ? (
        <div className="row">
          <button className="primary" onClick={() => void start("subscription")} disabled={busy}>
            {busy ? "Starting…" : "Sign in"}
          </button>
          <button className="ghost" onClick={() => void start("api_key")} disabled={busy}>
            Use an API key
          </button>
        </div>
      ) : null}
      {profile?.detail?.message && loggingIn && <p className="muted">{profile.detail.message}</p>}
      {profile && <LoginSteps profile={profile} onChanged={() => void refresh()} />}
    </div>
  );
}
