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
  agentCliStatus,
  onAgentCliStatus,
  type CliStatus,
  type Profile,
  type ProfileStatus,
} from "../ipc";
import {
  agentLoggedIn,
  createAgent,
  deleteAgent,
  getAgentLimits,
  getAgentUsage,
  type AgentUsage,
  renameAgent,
  type AgentLimit,
} from "../api/agents";
import { resetLabel } from "./UsageLimitBanner";
import { AssistantSettingsSection } from "./AssistantSettings";
import { ModelTiersSection } from "./ModelTiers";
import { trapFocus } from "./AppSettings";

type AgentsTab = "accounts" | "assistant" | "models";
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

function isActive(status: ProfileStatus): boolean {
  return [
    "starting",
    "waiting_for_browser",
    "waiting_for_code",
    "waiting_for_api_key",
    "verifying",
  ].includes(status);
}

/** Once an agent is connected, start its worker with the rest of the stack and
 * let work orders that paused on its expired login continue (ADR-017). */
export async function afterAgentConnected(agentId: string): Promise<void> {
  await stackUp();
  await agentLoggedIn(agentId);
}

/** The steps of a login in progress: the provider's login page and device
 * code, Claude's paste-back code, an API key, and Cancel. Renders nothing
 * while no login is active. */
export function LoginSteps({ profile, onChanged }: { profile: Profile; onChanged: () => void }) {
  const [code, setCode] = useState("");
  const [apiKey, setApiKey] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const openedUrl = useRef<string | null>(null);
  const s = profile.status;

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

  if (!isActive(s) && !profile.detail?.browserUrl) return null;
  return (
    <>
      {error && <div className="banner banner--error">{error}</div>}
      {profile.detail?.deviceCode && (
        <p className="profile-row__code">
          Enter this code on the page: <span className="mono">{profile.detail.deviceCode}</span>
        </p>
      )}
      {profile.detail?.browserUrl && isActive(s) && (
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
      {isActive(s) && (
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
    </>
  );
}

/** Agents (ADR-016): one per provider account. Each signs in once and can lead
 * or review any number of work orders. A new install has none (ADR-027): the
 * page starts with adding one. `focus` opens the assistant's tab. */
export function Agents({ onClose, focus }: { onClose: () => void; focus?: "assistant" }) {
  const [profiles, setProfiles] = useState<Profile[] | null>(null);
  const [limits, setLimits] = useState<AgentLimit[]>([]);
  const [usage, setUsage] = useState<AgentUsage[]>([]);
  const [error, setError] = useState<string | null>(null);
  // The agent just added goes straight on to signing in.
  const [added, setAdded] = useState<string | null>(null);
  const hasAgents = (profiles?.length ?? 0) > 0;
  const [tab, setTab] = useState<AgentsTab>(focus === "assistant" ? "assistant" : "accounts");
  const cardRef = useRef<HTMLDivElement | null>(null);
  const tabRefs = useRef<Partial<Record<AgentsTab, HTMLButtonElement | null>>>({});

  const refresh = useCallback(async () => {
    try {
      setProfiles(await listProfiles());
    } catch (e) {
      setError(String(e));
    }
    // Limits are a convenience; the agents show without them.
    getAgentLimits()
      .then((r) => setLimits(r.limits))
      .catch(() => undefined);
    getAgentUsage()
      .then((r) => setUsage(r.agents ?? []))
      .catch(() => undefined);
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
      if (p.status === "connected")
        void afterAgentConnected(p.profileId).catch((e) => setError(String(e)));
    });
    return () => {
      void unlisten.then((fn) => fn());
    };
  }, [refresh]);

  // Accounts, the assistant, and the model guide each get a tab, laid out
  // like Settings. Every panel stays mounted, so a sign-in in progress keeps
  // going while another tab is open.
  const tabs: { id: AgentsTab; label: string }[] = hasAgents
    ? [
        { id: "accounts", label: "Accounts" },
        { id: "assistant", label: "Assistant" },
        { id: "models", label: "Models" },
      ]
    : [{ id: "accounts", label: "Accounts" }];
  const shown = tabs.some((t) => t.id === tab) ? tab : "accounts";
  const onTabKey = (e: React.KeyboardEvent, index: number) => {
    const last = tabs.length - 1;
    const next =
      e.key === "ArrowDown" || e.key === "ArrowRight"
        ? index === last
          ? 0
          : index + 1
        : e.key === "ArrowUp" || e.key === "ArrowLeft"
          ? index === 0
            ? last
            : index - 1
          : e.key === "Home"
            ? 0
            : e.key === "End"
              ? last
              : null;
    if (next === null) return;
    e.preventDefault();
    setTab(tabs[next].id);
    tabRefs.current[tabs[next].id]?.focus();
  };

  const panels: Record<AgentsTab, React.ReactNode> = {
    accounts: (
      <>
        <p className="muted">
          Connect each agent to its Codex or Claude account. An agent signs in once and serves as
          lead or reviewer; secrets go straight to a private volume and never touch the coordinator.
        </p>
        <CliVersions />

        <div className="profiles">
          {profiles === null ? (
            <p className="muted">Loading…</p>
          ) : profiles.length === 0 ? (
            <div className="agents-empty">
              <strong>Add your first agent</strong>
              <p className="muted">
                An agent is one of your AI accounts: a Claude or ChatGPT (Codex) subscription, or an
                API key. Name it, choose which kind it is, then sign in. The assistant and all the
                work it starts run on your agents.
              </p>
            </div>
          ) : (
            profiles.map((p) => (
              <ProfileRow
                key={p.id}
                profile={p}
                limit={limits.find((l) => l.agent === p.id)}
                usage={usage.find((u) => u.agent === p.id)}
                signIn={p.id === added}
                onChanged={refresh}
                onAgentsChanged={changed}
              />
            ))
          )}
        </div>

        <AddAgent
          first={profiles !== null && profiles.length === 0}
          onAdded={async (id) => {
            setAdded(id);
            await changed();
          }}
        />
      </>
    ),
    assistant: hasAgents ? <AssistantSettingsSection /> : null,
    models: hasAgents ? <ModelTiersSection /> : null,
  };

  return (
    <div className="modal" onClick={onClose}>
      <div
        ref={cardRef}
        className="modal__card settings agents-dialog"
        role="dialog"
        aria-modal="true"
        aria-labelledby="agents-title"
        onClick={(e) => e.stopPropagation()}
        onKeyDown={(e) => {
          if (e.key === "Escape") {
            e.stopPropagation();
            onClose();
          } else if (e.key === "Tab") {
            trapFocus(e, cardRef.current);
          }
        }}
      >
        <div className="panel__head">
          <h2 id="agents-title">Agents</h2>
          <button className="ghost" onClick={onClose}>
            Close
          </button>
        </div>
        {error && <div className="banner banner--error">{error}</div>}

        <div className="settings__body">
          <div className="settings__tabs" role="tablist" aria-label="Agents sections">
            {tabs.map(({ id, label }, index) => (
              <button
                key={id}
                ref={(element) => {
                  tabRefs.current[id] = element;
                }}
                id={`agents-tab-${id}`}
                role="tab"
                aria-selected={shown === id}
                aria-controls={`agents-panel-${id}`}
                tabIndex={shown === id ? 0 : -1}
                className={`settings__tab ${shown === id ? "settings__tab--on" : ""}`}
                onClick={() => setTab(id)}
                onKeyDown={(e) => onTabKey(e, index)}
              >
                {label}
              </button>
            ))}
          </div>
          {tabs.map(({ id }) => (
            <div
              key={id}
              id={`agents-panel-${id}`}
              role="tabpanel"
              aria-labelledby={`agents-tab-${id}`}
              className="settings__panel"
              hidden={shown !== id}
              tabIndex={0}
            >
              {panels[id]}
            </div>
          ))}
        </div>
      </div>
    </div>
  );
}

// The agents' Codex and Claude Code versions. They update on their own; a
// restart onto a new version waits until no agent is running a turn.
function CliVersions() {
  const [statuses, setStatuses] = useState<CliStatus[]>([]);

  useEffect(() => {
    agentCliStatus()
      .then(setStatuses)
      .catch(() => undefined);
    const unlisten = onAgentCliStatus(setStatuses);
    return () => {
      void unlisten.then((fn) => fn());
    };
  }, []);

  if (statuses.length === 0) return null;
  return (
    <div className="cli-updates muted">
      {statuses.map((status) => {
        const name = status.provider === "claude" ? "Claude Code" : "Codex CLI";
        return (
          <div className="row cli-updates__row" key={status.provider}>
            <span>
              {status.state === "updating"
                ? `Updating ${name} to ${status.target}…`
                : status.state === "waiting"
                  ? `${name} update to ${status.target} waits for running work to finish.`
                  : status.state === "behind"
                    ? `${name} ${status.installed}; updating to ${status.target} failed and will be retried.`
                    : status.checked
                      ? `${name} ${status.installed} · up to date`
                      : `${name} ${status.installed} · couldn't check for updates (will retry)`}
            </span>
          </div>
        );
      })}
    </div>
  );
}

function AddAgent({
  first,
  onAdded,
}: {
  /** No agent exists yet: the form is the page's main action. */
  first: boolean;
  onAdded: (id: string) => Promise<void>;
}) {
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
      const created = await createAgent(name.trim(), provider);
      setName("");
      await onAdded(created.id);
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
          {first ? "Add an agent" : "Add agent"}
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
  limit,
  usage,
  signIn = false,
  onChanged,
  onAgentsChanged,
}: {
  profile: Profile;
  /** Just added: offer the ways to sign in at once. */
  signIn?: boolean;
  /** Set while the agent's usage limit pauses work. */
  limit?: AgentLimit;
  /** The account's usage as the agent last reported it. */
  usage?: AgentUsage;
  onChanged: () => void;
  onAgentsChanged: () => Promise<void>;
}) {
  const [renaming, setRenaming] = useState<string | null>(null);
  const [picking, setPicking] = useState(signIn && profile.status !== "connected");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const s = profile.status;
  const active = isActive(s);

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
      {limit && (
        <p className="profile-row__limit">
          Usage limit reached
          {resetLabel(limit.resets_at) ? ` · resets ${resetLabel(limit.resets_at)}` : ""} ·{" "}
          {limit.waiting} {limit.waiting === 1 ? "run waits" : "runs wait"} and continue by itself
        </p>
      )}
      {usage?.windows?.length ? <UsageBars usage={usage} /> : null}
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

      <LoginSteps profile={profile} onChanged={onChanged} />
    </div>
  );
}

// The account's usage limits, one bar per window, as the agent last reported
// them while working; providers only report during work, so it says when.
function UsageBars({ usage }: { usage: AgentUsage }) {
  return (
    <div className="usage-bars">
      {usage.windows.map((window) => {
        const used = window.used_percent;
        const reset = resetLabel(window.resets_at);
        return (
          <div key={window.name} className="usage-bars__row">
            <span className="usage-bars__name">{window.name}</span>
            <span className="usage-bars__track" aria-hidden>
              <span
                className={`usage-bars__fill ${window.limited || (used ?? 0) >= 90 ? "usage-bars__fill--high" : ""}`}
                style={{
                  width: `${Math.min(100, Math.max(0, used ?? (window.limited ? 100 : 0)))}%`,
                }}
              />
            </span>
            <span className="muted usage-bars__value">
              {used !== undefined ? `${Math.round(used)}%` : window.limited ? "limit reached" : "—"}
              {reset ? ` · resets ${reset}` : ""}
            </span>
          </div>
        );
      })}
      {usage.observed_at && (
        <p className="muted note usage-bars__seen">
          As of{" "}
          {new Date(usage.observed_at).toLocaleTimeString([], {
            hour: "2-digit",
            minute: "2-digit",
          })}
        </p>
      )}
    </div>
  );
}
