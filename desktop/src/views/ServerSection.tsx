import { useCallback, useEffect, useState } from "react";
import { connectedServer } from "../connection";
import {
  authentikPassword,
  connectSignIn,
  disconnectSignIn,
  installAuthentik,
  openExternal,
  hostUpdate,
  hostVersion,
  installHostUpdate,
  openPairing,
  pairedDevices,
  places as readPlaces,
  publishing as readPublishing,
  setDomain,
  setPlacement,
  setReach,
  PUBLISHING_CHANGED,
  moveServer,
  revealAppsPagePassword,
  setAppsPage,
  setListed,
  type Access,
  type Placement,
  type Records,
  type PublishingOverview,
  type ConnectSignIn,
  forgetServer,
  renameServer,
  revokeDevice,
  type HostUpdate,
  type PairedDevice,
  type Places,
} from "../ipc";
import { showPlace } from "./PlaceSwitch";
import { ConnectForm } from "./StartChoice";
import { startChatOn } from "../attention/askAssistant";
import { getAssistantSettings, type AssistantSettings } from "../api/assistant";
import { Agents } from "./Agents";
import { assistantGuidance, GUIDANCE, type AssistantGuidance } from "./setupGuidance";
import { reloadAgents, useAgentList } from "./useAgents";

/** This app's API version: raise it with `API_VERSION` in the host. */
const API_VERSION = 1;

function versionProblem(api: number): string | null {
  if (api === API_VERSION) return null;
  return api < API_VERSION
    ? "Your server is too old for this app. Update the server."
    : "This app is too old for your server. Update the app.";
}

/** Tells every part of the Server settings that publishing changed. */
function publishingChanged() {
  window.dispatchEvent(new Event(PUBLISHING_CHANGED));
}

/** Reads now, and again whenever publishing changes anywhere in Settings. */
function useFresh(refresh: () => void) {
  useEffect(() => {
    refresh();
    window.addEventListener(PUBLISHING_CHANGED, refresh);
    return () => window.removeEventListener(PUBLISHING_CHANGED, refresh);
  }, [refresh]);
}

/** Opens a new chat with Mayka about serving apps from this server. */
function askMaykaAboutServing(message: string, onLeave?: () => void) {
  startChatOn({ kind: "serving" }, message);
  onLeave?.();
}

/**
 * What Mayka still needs before she can answer on this server (ADR-027), and
 * whether that is known yet. Reloads after the Agents page closes.
 */
function useMaykaGuidance(): {
  guidance: AssistantGuidance;
  known: boolean;
  reload: () => void;
} {
  const agents = useAgentList();
  const [settings, setSettings] = useState<AssistantSettings | null>(null);
  const reload = useCallback(() => {
    void reloadAgents().catch(() => undefined);
    void getAssistantSettings()
      .then(setSettings)
      .catch(() => undefined);
  }, []);
  useEffect(reload, [reload]);
  const known = agents !== null && (agents.length === 0 || settings !== null);
  return { guidance: assistantGuidance(agents, settings), known, reload };
}

/**
 * Ask Mayka, or, while she has no AI account to work with, the way to give
 * her one: asking her then would only fail.
 */
function AskMayka({
  message,
  onLeave,
  onAsked,
  link,
}: {
  message: string;
  onLeave?: () => void;
  onAsked?: () => void;
  /** Shown as a link inside a sentence rather than a button. */
  link?: boolean;
}) {
  const { guidance, reload } = useMaykaGuidance();
  const [setUp, setSetUp] = useState(false);
  const className = link ? "shell-card__link" : undefined;
  if (guidance) {
    return (
      <>
        <button className={className} onClick={() => setSetUp(true)}>
          {GUIDANCE[guidance].action} first
        </button>
        {setUp && (
          <Agents
            focus={guidance === "set_up_assistant" ? "assistant" : undefined}
            onClose={() => {
              setSetUp(false);
              reload();
            }}
          />
        )}
      </>
    );
  }
  return (
    <button
      className={className}
      onClick={() => {
        onAsked?.();
        askMaykaAboutServing(message, onLeave);
      }}
    >
      Ask Mayka
    </button>
  );
}

// The servers this device knows, and the shown one's state, in Settings.
export function ServerSection({ running, onLeave }: { running: boolean; onLeave?: () => void }) {
  const server = connectedServer();
  return (
    <>
      {server && <ConnectedServer server={server} running={running} onLeave={onLeave} />}
      <Servers />
      <section className="set__section">
        <h3 className="set__heading">Connect a server</h3>
        <p className="muted set__note">
          Commitarium can run on a server of yours too, and its work carries on when this computer
          is off. This computer keeps working beside it.
        </p>
        <ConnectForm />
      </section>
    </>
  );
}

// The server's base domain, under which deployed apps get their addresses
// (PUBLISHING_PLAN.md), and how its DNS points here: one wildcard record,
// checked before the domain is kept, or a record for each address, each
// checked as it is used.
function DomainSection({ onLeave }: { onLeave?: () => void }) {
  const [overview, setOverview] = useState<PublishingOverview | null>(null);
  const [domain, setDomainText] = useState("");
  const [records, setRecords] = useState<Records>("wildcard");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const refresh = useCallback(() => {
    void readPublishing()
      .then((found) => {
        setOverview(found);
        setDomainText(found.domain ?? "");
        setRecords(found.records);
      })
      .catch((e) => setError(String(e)));
  }, []);
  useFresh(refresh);

  const save = async () => {
    setBusy(true);
    setError(null);
    try {
      await setDomain(domain, records);
      publishingChanged();
    } catch (e) {
      setError(String(e));
    } finally {
      setBusy(false);
    }
  };

  const shown = domain.trim().replace(/^\*\./, "") || "example.com";
  const address = overview?.server_address ?? "this server's address";
  // On a private network there is no domain: the Tailscale name stands in.
  if (overview?.reach === "private") return null;
  const changed = !overview?.domain || shown !== overview.domain || records !== overview.records;
  return (
    <section className="set__section">
      <h3 className="set__heading">Domain</h3>
      <p className="muted set__note">
        Deployed apps get an address under your domain, such as app.{shown}, and your DNS provider
        points those addresses at this server. Choose how:
      </p>
      <label className="set__row">
        <input
          type="radio"
          name="records"
          checked={records === "wildcard"}
          onChange={() => setRecords("wildcard")}
        />
        <span>One record for every app (recommended)</span>
      </label>
      {records === "wildcard" && (
        <p className="muted set__note">
          Add an A record for <code>*.{shown}</code> with the value <code>{address}</code>, once.
          Every app and the apps page then work without more DNS changes. Addresses that already
          have their own records, such as www.{shown}, keep them.
        </p>
      )}
      <label className="set__row">
        <input
          type="radio"
          name="records"
          checked={records === "each_name"}
          onChange={() => setRecords("each_name")}
        />
        <span>A record for each address</span>
      </label>
      {records === "each_name" && (
        <p className="muted set__note">
          For when you would rather not add a wildcard. Each address you use needs an A record with
          the value <code>{address}</code>, such as <code>apps.{shown}</code>. Commitarium tells you
          which record to add when you give an app its address.
        </p>
      )}
      <div className="set__row">
        <input
          aria-label="Domain"
          placeholder="example.com"
          value={domain}
          onChange={(e) => setDomainText(e.target.value)}
        />
        <button disabled={busy || !domain.trim() || !changed} onClick={() => void save()}>
          {busy ? "Checking…" : overview?.domain ? "Change" : "Use this domain"}
        </button>
      </div>
      {error && <p className="set__note">{error}</p>}
      <p className="muted set__note">
        Not sure how to do this at your DNS provider?{" "}
        <AskMayka
          link
          onLeave={onLeave}
          message={
            `Help me point ${domain.trim() ? `my domain ${shown}` : "my domain"} at this server ` +
            `(${address}), with ${records === "wildcard" ? "one wildcard record" : "a record for each address"}.`
          }
        />{" "}
        to walk you through it.
      </p>
    </section>
  );
}

const NO_PROVIDER: ConnectSignIn = { issuer: "", client_id: "", client_secret: "" };

// Company sign-in for deployed apps (COMPANY_SIGN_IN_PLAN.md): the company's
// provider, connected once for the server. Nothing of it runs until then.
function SignInSection({ onLeave }: { onLeave?: () => void }) {
  const [overview, setOverview] = useState<PublishingOverview | null>(null);
  const [asked, setAsked] = useState<ConnectSignIn>(NO_PROVIDER);
  const [editing, setEditing] = useState(false);
  const [installing, setInstalling] = useState(false);
  const [password, setPassword] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const refresh = useCallback(() => {
    void readPublishing()
      .then(setOverview)
      .catch((e) => setError(String(e)));
  }, []);
  useFresh(refresh);

  const run = async (work: () => Promise<unknown>) => {
    setBusy(true);
    setError(null);
    try {
      await work();
      publishingChanged();
      return true;
    } catch (e) {
      setError(String(e));
      return false;
    } finally {
      setBusy(false);
    }
  };

  if (!overview?.domain || overview.reach === "private") return null;
  const provider = overview.sign_in;
  const authentik = overview.authentik;
  const memory = authentik.memory_total_bytes;
  const memoryGb = memory === null ? null : Math.round(memory / 1024 ** 3);
  const install = () => {
    setInstalling(true);
    void run(installAuthentik).finally(() => setInstalling(false));
  };
  const redirect = provider?.redirect_url ?? `https://sign-in.${overview.domain}/oauth2/callback`;
  const field = (key: keyof ConnectSignIn) => ({
    value: asked[key] ?? "",
    autoCapitalize: "off",
    autoCorrect: "off",
    spellCheck: false,
    onChange: (e: React.ChangeEvent<HTMLInputElement>) =>
      setAsked((current) => ({ ...current, [key]: e.target.value })),
  });
  const connect = () =>
    void run(() => connectSignIn(asked)).then((done) => {
      if (!done) return;
      setEditing(false);
      setAsked(NO_PROVIDER);
    });

  return (
    <section className="set__section">
      <h3 className="set__heading">Company sign-in</h3>
      {provider && authentik.installed ? (
        <>
          <p className="set__note">
            Apps can let people sign in with their company account, through authentik on this server
            {authentik.running ? "" : " (starting)"}. Add people and groups in its admin page.
          </p>
          {authentik.admin_url && (
            <p className="muted set__note">
              Admin page:{" "}
              <button
                className="shell-card__link"
                onClick={() => void openExternal(authentik.admin_url!)}
              >
                {authentik.admin_url}
              </button>
              , user <code>{authentik.admin_user}</code>
              {password !== null && (
                <>
                  , password <code>{password}</code>
                </>
              )}
            </p>
          )}
          <div className="set__row">
            <button
              disabled={busy}
              onClick={() =>
                password === null
                  ? void run(() => authentikPassword().then(setPassword))
                  : setPassword(null)
              }
            >
              {password === null ? "Show password" : "Hide password"}
            </button>
            <button
              className="ghost"
              disabled={busy}
              title="Stops authentik; its people and groups stay for setting it up again"
              onClick={() => void run(disconnectSignIn)}
            >
              Disconnect
            </button>
          </div>
        </>
      ) : provider && !editing ? (
        <>
          <p className="set__note">
            Apps can let people sign in with their company account, through {provider.issuer}.
          </p>
          <p className="muted set__note">
            Registered sign-in address: <code>{redirect}</code>
          </p>
          <div className="set__row">
            <button
              disabled={busy}
              onClick={() => {
                setAsked({
                  ...NO_PROVIDER,
                  issuer: provider.issuer,
                  client_id: provider.client_id,
                });
                setEditing(true);
              }}
            >
              Change
            </button>
            <button className="ghost" disabled={busy} onClick={() => void run(disconnectSignIn)}>
              Disconnect
            </button>
          </div>
        </>
      ) : (
        <>
          <p className="muted set__note">
            Not sure where to start?{" "}
            <button
              className="shell-card__link"
              onClick={() => {
                startChatOn(
                  { kind: "sign_in" },
                  `Help me set up company sign-in for this server. Its domain is ${overview.domain}.`,
                );
                onLeave?.();
              }}
            >
              Ask Mayka
            </button>{" "}
            to help you choose and set it up.
          </p>
          <p className="muted set__note">
            Let people open deployed apps with their company account (Microsoft, Google, Okta,
            authentik and others), and choose which groups may open each app. In your company&apos;s
            sign-in provider, add an application for Commitarium with this sign-in address:{" "}
            <code>{redirect}</code>. Then give its details here.
          </p>
          <input
            aria-label="Issuer"
            placeholder="Issuer, such as https://login.example.com"
            {...field("issuer")}
          />
          <input aria-label="Client ID" placeholder="Client ID" {...field("client_id")} />
          <input
            aria-label="Client secret"
            placeholder="Client secret"
            type="password"
            {...field("client_secret")}
          />
          <details>
            <summary className="muted">More</summary>
            <input
              aria-label="Scopes"
              placeholder="Scopes (openid email profile)"
              {...field("scopes")}
            />
            <input
              aria-label="Groups claim"
              placeholder="Groups claim (groups)"
              {...field("groups_claim")}
            />
          </details>
          <div className="set__row">
            <button
              disabled={
                busy ||
                !asked.issuer.trim() ||
                !asked.client_id.trim() ||
                !asked.client_secret.trim()
              }
              onClick={connect}
            >
              {busy ? "Checking…" : "Connect"}
            </button>
            {editing && (
              <button className="ghost" onClick={() => setEditing(false)}>
                Cancel
              </button>
            )}
          </div>
          {!provider && (
            <>
              <h4 className="set__heading">No provider? Set up authentik here</h4>
              <p className="muted set__note">
                Commitarium can run authentik on this server and connect it for you: a full sign-in
                system with passwords, two-factor and passkeys, and an admin page for people and
                groups. It needs about {authentik.memory_needed_gb} GB of this server&apos;s memory
                for itself. A server of 8 GB runs it alongside Commitarium and a few apps; more
                gives your apps room to grow.
                {memoryGb !== null && ` This server has ${memoryGb} GB.`} A small team may prefer
                Pocket ID, which is lighter and which Mayka can help you set up.
              </p>
              {memoryGb !== null && memoryGb < 8 && (
                <p className="set__note">
                  This server is likely too small for authentik beside your apps: consider a bigger
                  one first.
                </p>
              )}
              <button disabled={busy} onClick={install}>
                {installing ? "Setting up authentik, a few minutes…" : "Set up authentik"}
              </button>
            </>
          )}
        </>
      )}
      {error && <p className="set__note">{error}</p>}
    </section>
  );
}

const PLACES: { value: Placement; label: string }[] = [
  { value: "home", label: "On a computer on my network" },
  { value: "cloud", label: "On a server in the cloud, or a VPS" },
  { value: "other", label: "Somewhere else, or not sure" },
];

// Asked once per server (HOMELAB_PLAN.md): where it is, so the app
// recommends how to serve its apps. It opens as a modal when a server that
// has not answered is shown, and stays in Settings → Server until it is
// answered. The answer is kept on the server.
function WhereSection({
  onLeave,
  modal,
  onClose,
  onNotNow,
  onOpenSettings,
}: {
  onLeave?: () => void;
  /** Shown as the modal on opening the server, rather than in Settings. */
  modal?: boolean;
  onClose?: () => void;
  /** Puts the question off on this computer. */
  onNotNow?: () => void;
  onOpenSettings?: () => void;
}) {
  const [overview, setOverview] = useState<PublishingOverview | null>(null);
  const [answered, setAnswered] = useState<Placement | null>(null);
  const [asking, setAsking] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const refresh = useCallback(() => {
    void readPublishing()
      .then(setOverview)
      .catch((e) => setError(String(e)));
  }, []);
  useFresh(refresh);

  const run = async (work: () => Promise<unknown>) => {
    setBusy(true);
    setError(null);
    try {
      await work();
      publishingChanged();
    } catch (e) {
      setError(String(e));
    } finally {
      setBusy(false);
    }
  };
  const answer = (placement: Placement) =>
    void run(async () => {
      await setPlacement(placement);
      setAnswered(placement);
    });

  // Asked once: a server already serving apps, or already answered, skips
  // it. Once shown it stays, so Back can change an answer just kept.
  const fresh =
    overview !== null &&
    overview.placement === null &&
    !overview.domain &&
    overview.reach !== "private";
  if (fresh && !asking) setAsking(true);
  if (!overview || !(fresh || asking)) return null;
  // In the modal, the settings it recommends are a click away; in Settings,
  // they are the sections below.
  const where = (section: string) => (modal ? `in Settings → Server → ${section}` : "below");
  const toSettings = modal && onOpenSettings && (
    <button
      className="ghost"
      disabled={busy}
      onClick={() => {
        onClose?.();
        onOpenSettings();
      }}
    >
      Open Settings
    </button>
  );
  const back = (
    <button className="ghost" disabled={busy} onClick={() => setAnswered(null)}>
      Back
    </button>
  );
  const content = (
    <>
      {answered === null ? (
        <>
          <p className="muted set__note">
            So Commitarium can recommend how your apps reach you and others. You can change anything
            later.
          </p>
          <div className="set__actions">
            {PLACES.map((place) => (
              <button key={place.value} disabled={busy} onClick={() => answer(place.value)}>
                {place.label}
              </button>
            ))}
          </div>
          {modal && (
            <div className="set__actions">
              <button className="ghost" onClick={onNotNow}>
                Not now
              </button>
            </div>
          )}
          {modal && (
            <p className="muted set__note">You can answer this any time in Settings → Server.</p>
          )}
        </>
      ) : answered === "home" ? (
        <>
          <p className="set__note">
            Recommended: <strong>Homelab mode</strong>. Your apps are on a private network that only
            your own devices reach, through Tailscale, at home and away. No domain, nothing open on
            your router.
          </p>
          <div className="set__actions">
            {overview.reach === "private" ? (
              <p className="muted set__note">
                Homelab mode is on: its steps are {where("Homelab")}.
              </p>
            ) : (
              <button disabled={busy} onClick={() => void run(() => setReach("private"))}>
                Turn on Homelab mode
              </button>
            )}
            {toSettings}
            {back}
          </div>
          <p className="muted set__note">You can change this later {where("Homelab")}.</p>
        </>
      ) : answered === "cloud" ? (
        <>
          <p className="set__note">
            Recommended: <strong>your own domain</strong>. Your apps get addresses such as
            app.yourcompany.com, for anyone on the internet, each open, behind a password, or behind
            company sign-in. Set it {where("Domain")}: it explains the DNS record to add.
          </p>
          <div className="set__actions">
            {toSettings}
            {back}
          </div>
        </>
      ) : (
        <>
          <p className="set__note">
            Mayka can help you choose: tell her where it runs and who should open your apps.
          </p>
          <div className="set__actions">
            <AskMayka
              onLeave={onLeave}
              onAsked={onClose}
              message="Help me choose how to serve apps from my Commitarium server."
            />
            {back}
          </div>
        </>
      )}
      {modal && answered !== null && (
        <div className="set__actions">
          <button className="ghost" onClick={onClose}>
            Done
          </button>
        </div>
      )}
      {error && <p className="set__note">{error}</p>}
    </>
  );
  if (modal) {
    return (
      <div className="modal">
        <div className="modal__card set__section" role="dialog" aria-label="Where is this server?">
          <div className="panel__head">
            <h2>Where is this server?</h2>
          </div>
          {content}
        </div>
      </div>
    );
  }
  return (
    <section className="set__section">
      <h3 className="set__heading">Where is this server?</h3>
      {content}
    </section>
  );
}

const WHERE_DISMISSED = "commitarium:where-not-now:";

/**
 * The first time a server opens on this computer: Mayka's AI account, if
 * the server has none yet, since everything else leans on her; then where
 * the server is. Each step can be put off; Not now on the question keeps it
 * in Settings → Server only.
 */
export function ServerQuestion({ onOpenSettings }: { onOpenSettings: () => void }) {
  const server = connectedServer();
  const { guidance, known, reload } = useMaykaGuidance();
  const [closed, setClosed] = useState(() => {
    try {
      return server !== null && localStorage.getItem(WHERE_DISMISSED + server) !== null;
    } catch {
      return false;
    }
  });
  // Mayka's step, shown once while this window is open.
  const [maykaDone, setMaykaDone] = useState(false);
  const [agentsOpen, setAgentsOpen] = useState(false);
  if (!server || closed || !known) return null;
  // Only Not now puts it off on this computer; an answer is kept on the
  // server, and asking Mayka carries on in her chat.
  const close = (notNow: boolean) => {
    if (notNow) {
      try {
        localStorage.setItem(WHERE_DISMISSED + server, "1");
      } catch {
        // Without storage it may ask again next time: harmless.
      }
    }
    setClosed(true);
  };
  if (agentsOpen) {
    return (
      <Agents
        focus={guidance === "set_up_assistant" ? "assistant" : undefined}
        onClose={() => {
          setAgentsOpen(false);
          setMaykaDone(true);
          reload();
        }}
      />
    );
  }
  if (guidance && !maykaDone) {
    return (
      <div className="modal">
        <div className="modal__card set__section" role="dialog" aria-label="Set up Mayka">
          <div className="panel__head">
            <h2>{guidance === "add_agent" ? "Connect an AI account" : "Set up Mayka"}</h2>
          </div>
          <p className="set__note">{GUIDANCE[guidance].text}</p>
          <div className="set__actions">
            <button className="primary" onClick={() => setAgentsOpen(true)}>
              {GUIDANCE[guidance].action}
            </button>
            <button className="ghost" onClick={() => setMaykaDone(true)}>
              Not now
            </button>
          </div>
          <p className="muted set__note">
            Until then Mayka cannot answer, here or in a chat. You can do this any time from Agents.
          </p>
        </div>
      </div>
    );
  }
  return (
    <WhereSection
      modal
      onClose={() => close(false)}
      onNotNow={() => close(true)}
      onOpenSettings={onOpenSettings}
    />
  );
}

// Homelab mode (HOMELAB_PLAN.md): off, apps are served at the server's own
// domain, for anyone on the internet; on, only the owner's devices reach
// them, on their Tailscale network, with the steps Tailscale takes.
function HomelabSection({ onLeave }: { onLeave?: () => void }) {
  const [overview, setOverview] = useState<PublishingOverview | null>(null);
  const [asked, setAsked] = useState<boolean | null>(null);
  // Turning off would cut this computer off: why, and the way back it tried.
  const [stranded, setStranded] = useState<{ tried: string | null } | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [moved, setMoved] = useState(false);

  const refresh = useCallback(() => {
    void readPublishing()
      .then(setOverview)
      .catch((e) => setError(String(e)));
  }, []);
  useFresh(refresh);
  const waiting = overview?.reach === "private" && overview.tailscale?.state !== "ready";
  // Signing in and turning on HTTPS happen elsewhere: look again while waiting.
  useEffect(() => {
    if (!waiting) return;
    const timer = setInterval(refresh, 4000);
    return () => clearInterval(timer);
  }, [waiting, refresh]);

  if (!overview) return null;
  const on = overview.reach === "private";
  const tailscale = overview.tailscale;
  const switching = asked !== null && asked !== on;
  const server = connectedServer();
  const onTailscale =
    Boolean(tailscale?.address && server?.startsWith(tailscale.address + ":")) || moved;
  const change = async (anyway = false) => {
    if (asked === null) return;
    setBusy(true);
    setError(null);
    try {
      // Off stops the server's Tailscale: a computer that reaches the server
      // through it goes back to the address it had first, or is told what
      // losing the server means before it does.
      if (!asked && onTailscale && !anyway) {
        const found = await readPlaces();
        const shown = found.servers.find((s) => s.fingerprint === found.active);
        const back = shown?.previous_address ?? null;
        const movedBack =
          back !== null &&
          found.active !== null &&
          (await moveServer(found.active, back).then(
            () => true,
            () => false,
          ));
        if (!movedBack) {
          setStranded({ tried: back });
          return;
        }
        setMoved(false);
      }
      await setReach(asked ? "private" : "internet");
      setAsked(null);
      setStranded(null);
      publishingChanged();
    } catch (e) {
      setError(String(e));
    } finally {
      setBusy(false);
    }
  };
  const moveHere = async () => {
    if (!tailscale?.address) return;
    setBusy(true);
    setError(null);
    try {
      const found = await readPlaces();
      if (!found.active) throw new Error("This computer is not showing a server.");
      await moveServer(found.active, `${tailscale.address}:7443`);
      setMoved(true);
    } catch (e) {
      setError(String(e));
    } finally {
      setBusy(false);
    }
  };

  return (
    <section className="set__section">
      <h3 className="set__heading">Homelab</h3>
      <label className="set__row">
        <input
          type="checkbox"
          checked={asked ?? on}
          disabled={busy}
          onChange={(e) => {
            setAsked(e.target.checked);
            setStranded(null);
          }}
        />
        <span>Use this server as my homelab</span>
      </label>
      {!switching && !on && (
        <p className="muted set__note">
          For a computer at home: your apps go on a private network that only your own devices
          reach, through Tailscale, with no domain and nothing open on your router. Off, they are
          served at your own domain, for anyone on the internet.
        </p>
      )}
      {switching && stranded && (
        <>
          <p className="set__note">
            This computer reaches the server only through Tailscale
            {stranded.tried
              ? `, and it could not reach it at its home network address (${stranded.tried}) just now`
              : ""}
            . Turning Homelab off stops Tailscale on the server, so this computer loses the server.
            To connect again you would need to be on the same network as the server, run{" "}
            <code>sudo commitarium-host pair</code> on it, and paste the line it prints into
            Commitarium.
            {stranded.tried && " If you are away from home, turning it off from home avoids this."}
          </p>
          <p className="muted set__note">
            Not sure what you need? Mayka can help you find the way that fits.
          </p>
          <div className="set__actions">
            <AskMayka
              onLeave={onLeave}
              message={
                "I want to turn off Homelab mode on my server, but this computer reaches it only " +
                "through Tailscale, so it would lose the server. Help me work out what to do."
              }
            />
            <button className="ghost" disabled={busy} onClick={() => void change(true)}>
              Turn off anyway
            </button>
            <button
              className="ghost"
              disabled={busy}
              onClick={() => {
                setStranded(null);
                setAsked(null);
              }}
            >
              Cancel
            </button>
          </div>
        </>
      )}
      {switching && !stranded && (
        <>
          <p className="set__note">
            {asked
              ? "Your apps move to a private network that only your devices reach, through Tailscale: no domain, nothing open on your router. Every app gets a new address, and company sign-in is not offered there."
              : "Your apps move to addresses under your own domain, for anyone on the internet. Every app gets a new address."}
            {overview.apps.length > 0 && " Anyone with an old link needs the new one."}
            {!asked &&
              onTailscale &&
              " This computer first goes back to reaching the server at its home network address."}
          </p>
          <div className="set__row">
            <button disabled={busy} onClick={() => void change()}>
              {busy ? "Switching…" : asked ? "Turn on" : "Turn off"}
            </button>
            <button className="ghost" disabled={busy} onClick={() => setAsked(null)}>
              Cancel
            </button>
          </div>
        </>
      )}
      {!switching && on && (
        <TailscaleSteps
          tailscale={tailscale}
          busy={busy}
          onTailscale={onTailscale}
          onMove={() => void moveHere()}
        />
      )}
      {error && <p className="set__note">{error}</p>}
    </section>
  );
}

function TailscaleSteps({
  tailscale,
  busy,
  onTailscale,
  onMove,
}: {
  tailscale: PublishingOverview["tailscale"];
  busy: boolean;
  onTailscale: boolean;
  onMove: () => void;
}) {
  switch (tailscale?.state) {
    case "needs_login":
      return (
        <>
          <p className="set__note">
            Sign in to Tailscale to put this server on your private network. A free personal account
            is enough; use the same account on your phone and computers.
          </p>
          {tailscale.login_url ? (
            <button onClick={() => void openExternal(tailscale.login_url!)}>
              Sign in to Tailscale ↗
            </button>
          ) : (
            <p className="muted set__note">Getting the sign-in link…</p>
          )}
        </>
      );
    case "needs_https":
      return (
        <>
          <p className="set__note">
            Signed in{tailscale.name ? ` as ${tailscale.name}` : ""}. One more step, once: in
            Tailscale&apos;s DNS settings, turn on <strong>MagicDNS</strong> and{" "}
            <strong>HTTPS Certificates</strong>, so your browsers trust your apps&apos; addresses.
          </p>
          <button onClick={() => void openExternal(tailscale.https_settings)}>
            Open Tailscale&apos;s DNS settings ↗
          </button>
        </>
      );
    case "system_signed_out":
      return (
        <p className="set__note">
          This server runs Tailscale itself, and it is signed out. On the server, run{" "}
          <code>sudo tailscale up</code> and sign in.
        </p>
      );
    case "ready":
      return (
        <>
          <p className="set__note">
            Your apps are on your private network at{" "}
            <button
              className="shell-card__link"
              onClick={() => void openExternal(`https://${tailscale.name}`)}
            >
              {tailscale.name} ↗
            </button>
            . To open them, install Tailscale on your phone and computers and sign in with the same
            account.
          </p>
          {onTailscale ? (
            <p className="muted set__note">
              This computer reaches the server through Tailscale, so it works away from home too.
            </p>
          ) : (
            <>
              <p className="muted set__note">
                This computer reaches the server at its home network address, which works only at
                home. With Tailscale on this computer, it can reach it anywhere.
              </p>
              <button disabled={busy} onClick={onMove}>
                Connect through Tailscale
              </button>
            </>
          )}
        </>
      );
    default:
      return <p className="muted set__note">Starting Tailscale on the server…</p>;
  }
}

const ACCESS_CHOICES: { value: Access; label: string }[] = [
  { value: "public", label: "Anyone, no password" },
  { value: "private", label: "Only with a password" },
  { value: "company", label: "Company sign-in" },
];

// The apps page (APPS_PAGE_PLAN.md): apps.<domain> lists this server's apps
// and opens them, for the people who use them. Off until turned on.
function AppsPageSection() {
  const [overview, setOverview] = useState<PublishingOverview | null>(null);
  const [access, setAccess] = useState<Access>("private");
  const [password, setPassword] = useState("");
  const [askPassword, setAskPassword] = useState(false);
  const [shownPassword, setShownPassword] = useState<string | null | undefined>(undefined);
  const [name, setName] = useState("apps");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const refresh = useCallback(() => {
    void readPublishing()
      .then((found) => {
        setOverview(found);
        setName(found.apps_page?.name ?? "apps");
        setAccess(found.apps_page?.access ?? (found.sign_in ? "company" : "private"));
      })
      .catch((e) => setError(String(e)));
  }, []);
  useFresh(refresh);

  const run = async (work: () => Promise<unknown>) => {
    setBusy(true);
    setError(null);
    try {
      await work();
      setPassword("");
      setAskPassword(false);
      publishingChanged();
    } catch (e) {
      setError(String(e));
    } finally {
      setBusy(false);
    }
  };

  const onTailnet = overview?.reach === "private";
  if (!overview?.domain && !onTailnet) return null;
  if (!overview) return null;
  const page = overview.apps_page;
  const shownName = name.trim().toLowerCase() || "apps";
  // On the private network the page has the Tailscale name to itself.
  const renamed = !onTailnet && page !== null && shownName !== page.name;
  const choices = ACCESS_CHOICES.filter(
    (choice) =>
      choice.value !== "company" ||
      (!onTailnet && (overview.sign_in || page?.access === "company")),
  );
  // A password page needs one the first time; after that it keeps its own.
  const choose = (next: Access) => {
    setAccess(next);
    if (!page) return;
    if (next === "private") {
      // It keeps the password it had; without one, ask for it.
      void setAppsPage("private")
        .then(refresh)
        .catch(() => setAskPassword(true));
      return;
    }
    void run(() => setAppsPage(next));
  };
  const passwordField = (
    <div className="set__row">
      <input
        aria-label="Apps page password"
        placeholder="A password to share"
        autoCapitalize="off"
        autoCorrect="off"
        spellCheck={false}
        value={password}
        onChange={(e) => setPassword(e.target.value)}
      />
      {page && (
        <button
          disabled={busy || !password}
          onClick={() => void run(() => setAppsPage("private", password))}
        >
          Save
        </button>
      )}
    </div>
  );

  return (
    <section className="set__section">
      <h3 className="set__heading">Apps page</h3>
      <p className="muted set__note">
        One address that lists this server&apos;s apps and opens them, in any browser and on a
        phone. People use the apps there; nothing of Commitarium is shown.
      </p>
      <label className="muted" hidden={onTailnet}>
        Address
        <span className="deploy-address__name">
          <input
            aria-label="Apps page address"
            value={name}
            autoCapitalize="off"
            autoCorrect="off"
            spellCheck={false}
            onChange={(e) => setName(e.target.value)}
          />
          <span className="muted">.{overview.domain}</span>
        </span>
      </label>
      {renamed && (
        <>
          <p className="sync-card__error">
            {page.url.replace("https://", "")} stops working once you change it. Anyone with the old
            link needs the new one.
          </p>
          <div className="set__row">
            <button
              disabled={busy || !name.trim()}
              onClick={() => void run(() => setAppsPage(page.access, undefined, name))}
            >
              Change address
            </button>
            <button className="ghost" onClick={() => setName(page.name)}>
              Cancel
            </button>
          </div>
        </>
      )}
      {page && (
        <p className="set__note">
          On at{" "}
          <button className="shell-card__link" onClick={() => void openExternal(page.url)}>
            {page.url.replace("https://", "")} ↗
          </button>
        </p>
      )}
      <label className="muted deploy-access">
        Who can open it
        <select value={access} disabled={busy} onChange={(e) => choose(e.target.value as Access)}>
          {choices.map((choice) => (
            <option key={choice.value} value={choice.value}>
              {choice.label}
            </option>
          ))}
        </select>
      </label>
      {access === "company" && (
        <p className="muted set__note">
          Anyone at the company can open it, and each person sees only the apps they can open. Apps
          behind a password are not listed there.
        </p>
      )}
      {access === "private" && (
        <p className="muted set__note">
          People sign in with the username <strong>{page?.name ?? shownName}</strong> and the
          password you choose.
        </p>
      )}
      {access === "private" && (!page || askPassword) && passwordField}
      {page?.access === "private" && !askPassword && (
        <div className="set__row">
          <button
            className="ghost"
            disabled={busy}
            onClick={() =>
              shownPassword === undefined
                ? void revealAppsPagePassword().then(setShownPassword)
                : setShownPassword(undefined)
            }
          >
            {shownPassword === undefined ? "Show password" : "Hide password"}
          </button>
          <button className="ghost" disabled={busy} onClick={() => setAskPassword(true)}>
            Change password
          </button>
          {shownPassword !== undefined && <code>{shownPassword ?? "not kept"}</code>}
        </div>
      )}
      {page && overview.apps.length > 0 && (
        <>
          <h4 className="set__heading">Listed there</h4>
          {overview.apps.map((app) => (
            <label key={app.project_id} className="set__row">
              <input
                type="checkbox"
                checked={app.listed}
                disabled={busy}
                onChange={(e) => void run(() => setListed(app.project_id, e.target.checked))}
              />
              <span>
                {app.name}
                {!app.live && <span className="muted"> · not running</span>}
              </span>
            </label>
          ))}
        </>
      )}
      <div className="set__row">
        {page ? (
          <button
            className="ghost"
            disabled={busy}
            onClick={() => void run(() => setAppsPage(null))}
          >
            Turn off
          </button>
        ) : (
          <button
            disabled={busy || !name.trim() || (access === "private" && !password)}
            onClick={() => void run(() => setAppsPage(access, password, name))}
          >
            Turn on
          </button>
        )}
      </div>
      {error && <p className="set__note">{error}</p>}
    </section>
  );
}

// Every paired server: show it, name it, or forget it on this device.
function Servers() {
  const [known, setKnown] = useState<Places | null>(null);
  const [naming, setNaming] = useState<string | null>(null);
  const [name, setName] = useState("");
  const [error, setError] = useState<string | null>(null);

  const refresh = useCallback(() => {
    void readPlaces()
      .then(setKnown)
      .catch((e) => setError(String(e)));
  }, []);
  useEffect(refresh, [refresh]);

  if (!known || known.servers.length === 0) return null;
  const act = (work: Promise<unknown>) => work.then(refresh).catch((e) => setError(String(e)));
  const save = (fingerprint: string) => {
    setNaming(null);
    act(renameServer(fingerprint, name));
  };
  const forget = (fingerprint: string, label: string) => {
    if (!window.confirm(`Forget ${label} on this computer? Pairing it again needs a new code.`))
      return;
    const shown = known.active === fingerprint;
    void forgetServer(fingerprint)
      .then(() => (shown ? window.location.reload() : refresh()))
      .catch((e) => setError(String(e)));
  };

  return (
    <section className="set__section">
      <h3 className="set__heading">Servers</h3>
      {known.servers.map((server) => (
        <div className="set__row" key={server.fingerprint}>
          {naming === server.fingerprint ? (
            <input
              autoFocus
              aria-label="Server name"
              value={name}
              onChange={(e) => setName(e.target.value)}
              onKeyDown={(e) => {
                if (e.key === "Enter") save(server.fingerprint);
                if (e.key === "Escape") setNaming(null);
              }}
              onBlur={() => save(server.fingerprint)}
            />
          ) : (
            <span title={server.address}>
              {server.name}
              {known.active === server.fingerprint && <span className="muted"> · shown</span>}
            </span>
          )}
          {known.active !== server.fingerprint && (
            <button onClick={() => void showPlace(server.fingerprint)}>Show</button>
          )}
          <button
            className="ghost"
            onClick={() => {
              setName(server.name === server.address ? "" : server.name);
              setNaming(server.fingerprint);
            }}
          >
            Rename
          </button>
          <button className="ghost" onClick={() => forget(server.fingerprint, server.name)}>
            Forget
          </button>
        </div>
      ))}
      {error && <p className="set__note">{error}</p>}
    </section>
  );
}

function ConnectedServer({
  server,
  running,
  onLeave,
}: {
  server: string;
  running: boolean;
  onLeave?: () => void;
}) {
  const [problem, setProblem] = useState<string | null>(null);
  const [update, setUpdate] = useState<HostUpdate | null>(null);
  const [devices, setDevices] = useState<PairedDevice[]>([]);
  const [line, setLine] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [updating, setUpdating] = useState(false);

  const refresh = useCallback(() => {
    void hostVersion()
      .then((version) => setProblem(versionProblem(version.api_version)))
      .catch((e) => setError(String(e)));
    void hostUpdate()
      .then(setUpdate)
      .catch(() => setUpdate(null));
    void pairedDevices()
      .then(setDevices)
      .catch((e) => setError(String(e)));
  }, []);

  useEffect(refresh, [refresh]);

  const act = (work: Promise<unknown>) => work.then(refresh).catch((e) => setError(String(e)));

  const addDevice = async () => {
    try {
      const opened = await openPairing();
      const address = server.replace(/:\d+$/, "");
      setLine(`commitarium://${address}:${opened.port}#${opened.code}`);
    } catch (e) {
      setError(String(e));
    }
  };

  const install = async () => {
    setUpdating(true);
    setError(null);
    try {
      await installHostUpdate();
      // The server restarts itself, and the app finds it again.
      setUpdate(null);
      window.dispatchEvent(new Event(SERVER_UPDATED));
    } catch (e) {
      setError(String(e));
    }
    setUpdating(false);
  };

  return (
    <>
      <section className="set__section">
        <h3 className="set__heading">Server</h3>
        <p className="set__note">Connected to {server}</p>
        {problem && <p className="set__note">{problem}</p>}
        {update?.available && (
          <>
            <p className="set__note">
              Update available: {update.current} to {update.latest}
            </p>
            <button disabled={running || updating} onClick={() => void install()}>
              {updating ? "Updating…" : "Update server"}
            </button>
            {running && <p className="muted set__note">Waits until work orders finish.</p>}
          </>
        )}
        {update && !update.available && (
          <p className="muted set__note">Server is up to date ({update.current}).</p>
        )}
        <button onClick={() => void showPlace(null)}>Show this computer</button>
      </section>

      <WhereSection onLeave={onLeave} />
      <HomelabSection onLeave={onLeave} />
      <DomainSection onLeave={onLeave} />
      <SignInSection onLeave={onLeave} />
      <AppsPageSection />

      <section className="set__section">
        <h3 className="set__heading">Devices</h3>
        {devices.map((device) => (
          <div className="set__row" key={device.fingerprint}>
            <span>{device.name}</span>
            <button onClick={() => void act(revokeDevice(device.fingerprint))}>Revoke</button>
          </div>
        ))}
        <button onClick={() => void addDevice()}>Add device</button>
        {line && (
          <>
            <p className="muted set__note">
              Paste this into Commitarium on the other device. It works once, for 5 minutes.
            </p>
            <code className="boot__command">{line}</code>
          </>
        )}
      </section>
      {error && <p className="set__note">{error}</p>}
    </>
  );
}

const SERVER_UPDATED = "commitarium:server-updated";

// Tells the user when their server has an update, wherever they are.
export function ServerBanner({ onOpen }: { onOpen: () => void }) {
  const [latest, setLatest] = useState<string | null>(null);

  useEffect(() => {
    if (!connectedServer()) return;
    const check = () =>
      void hostUpdate()
        .then((update) => setLatest(update.available ? update.latest : null))
        .catch(() => setLatest(null));
    // Once an update starts, the banner has nothing left to say.
    const updated = () => setLatest(null);
    check();
    window.addEventListener(SERVER_UPDATED, updated);
    return () => window.removeEventListener(SERVER_UPDATED, updated);
  }, []);

  if (!latest) return null;
  return (
    <div className="banner banner--warn coordinator-banner" role="status">
      <span>Update available for your server: {latest}</span>
      <button className="ghost" onClick={onOpen}>
        View
      </button>
    </div>
  );
}
