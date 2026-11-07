import { Fragment, useCallback, useEffect, useState } from "react";
import { connectedServer, TAILSCALE_HINT, throughTailscale } from "../connection";
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
  addDomain,
  setDomain,
  removeDomain,
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
import { confirmAction, PLACES_CHANGED } from "../confirm";
import { ConnectForm } from "./StartChoice";
import { startChatOn } from "../attention/askAssistant";
import { getAssistantSettings, type AssistantSettings } from "../api/assistant";
import { Agents } from "./Agents";
import { assistantGuidance, GUIDANCE, type AssistantGuidance } from "./setupGuidance";
import { reloadAgents, useAgentList } from "./useAgents";
import { appsOn, authentikHome, recordsFor, urlsOn } from "./domainRecords";

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

/** A ? that explains on hover, focus or click. */
function Hint({ label, children }: { label: string; children: React.ReactNode }) {
  const [open, setOpen] = useState(false);
  return (
    <span className="hint" data-open={open || undefined}>
      <button
        type="button"
        className="hint__mark"
        aria-label={label}
        aria-expanded={open}
        onClick={() => setOpen(!open)}
        onBlur={() => setOpen(false)}
      >
        ?
      </button>
      <span role="tooltip" className="hint__tip">
        {children}
      </span>
    </span>
  );
}

const RECORDS_LABEL: Record<Records, string> = {
  wildcard: "Wildcard record",
  each_name: "A record per app",
};

/** Reads publishing now and whenever it changes in Settings. */
function usePublishing(): PublishingOverview | null {
  const [overview, setOverview] = useState<PublishingOverview | null>(null);
  const refresh = useCallback(() => {
    void readPublishing()
      .then(setOverview)
      .catch(() => undefined);
  }, []);
  useFresh(refresh);
  return overview;
}

/** A link that opens an address in the browser, shown without https://. */
function Opens({ url }: { url: string }) {
  return (
    <button className="shell-card__link" onClick={() => void openExternal(url)}>
      {url.replace("https://", "")} ↗
    </button>
  );
}

// The domains apps are served on (MULTI_DOMAIN_PLAN.md): each with its DNS
// mode, the records it needs, and the apps that live there. The first one is
// set and renamed as the server's one domain was; more are added beside it.
function DomainSection({ onLeave }: { onLeave?: () => void }) {
  const [overview, setOverview] = useState<PublishingOverview | null>(null);
  // The domain being added or edited (`editing`: the one it was), if the form is open.
  const [form, setForm] = useState<{
    domain: string;
    records: Records;
    editing: string | null;
  } | null>(null);
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

  // On a private network there is no domain: the Tailscale name stands in.
  if (!overview || overview.reach === "private") return null;
  const domains = overview.domains;
  const first = domains[0]?.name ?? null;
  const address = overview.server_address ?? "this server's address";
  const shown = form?.domain.trim().replace(/^\*\./, "") || "example.com";
  const home = authentikHome(overview.authentik.admin_url, domains);
  // The first domain is set (and renamed) as the one domain always was, so a
  // server from before several domains takes it; the others are added.
  const save = () =>
    form &&
    void run(() =>
      form.editing !== null || domains.length === 0
        ? setDomain(form.domain, form.records)
        : addDomain(form.domain, form.records),
    ).then((ok) => ok && setForm(null));
  const several =
    domains.length > 1 || (form !== null && form.editing === null && domains.length > 0);

  return (
    <section className="set__section">
      <h3 className="set__heading set__heading--hint">
        Domains
        <Hint label="About domains">
          A domain you own, such as example.com. Each app you deploy gets an address under it, like
          app.example.com, or the domain itself for a website. You need access to its DNS settings.
          No domain? Buy one from any registrar, or use Homelab for a server at home.
        </Hint>
      </h3>
      {domains.length === 0 && !form && <p className="muted set__note">No domain yet.</p>}
      {domains.map((item) => {
        const apps = appsOn(item.name, overview.apps);
        const records = recordsFor(item, apps, home === item.name);
        // With a record for each address: the apps whose address has none yet.
        const missing = apps.filter((app) => app.record_problem?.includes("does not point"));
        return (
          <div className="domain-block" key={item.name}>
            <div className="set__row domain-row">
              <span className="domain-row__name">{item.name}</span>
              <span className="muted domain-row__records">{RECORDS_LABEL[item.records]}</span>
              {item.name === first && (
                <button
                  className="ghost"
                  disabled={busy || form !== null}
                  onClick={() =>
                    setForm({ domain: item.name, records: item.records, editing: item.name })
                  }
                >
                  Edit
                </button>
              )}
              <button
                className="ghost"
                disabled={busy || form !== null}
                onClick={() => void run(() => removeDomain(item.name))}
              >
                Remove
              </button>
            </div>
            {records.length > 0 && (
              <p className="muted set__note">
                DNS: an A record for{" "}
                {records.map((name, index) => (
                  <span key={name}>
                    {index > 0 && (index === records.length - 1 ? " and " : ", ")}
                    <code>{name}</code>
                  </span>
                ))}
                , each with the value <code>{address}</code>.
                {records.includes("@") &&
                  item.records === "wildcard" &&
                  " A wildcard does not cover the domain itself, so its website needs @ too."}
              </p>
            )}
            {missing.length > 0 && !form && (
              <div className="set__note">
                <p>Missing at your DNS provider, so these do not open yet:</p>
                <ul className="domain-missing">
                  {missing.map((app) => (
                    <li key={app.project_id}>{app.record_problem}</li>
                  ))}
                </ul>
              </div>
            )}
            {apps.length > 0 && (
              <ul className="domain-apps">
                {apps.map((app) => (
                  <li key={app.project_id}>
                    <span
                      className={`dot dot--${app.live ? "ok" : "muted"}`}
                      aria-label={app.live ? "Live" : "Not running"}
                    />{" "}
                    {urlsOn(item.name, app).map((url, index) => (
                      <span key={url}>
                        {index > 0 && ", "}
                        <Opens url={url} />
                      </span>
                    ))}
                    {!app.live && <span className="muted"> · not running</span>}
                  </li>
                ))}
              </ul>
            )}
          </div>
        );
      })}
      {domains.length > 0 && (
        <p className="muted set__note">
          With Cloudflare, set each record to DNS only (grey cloud), not proxied: Commitarium gets
          each address&apos;s certificate itself, and the proxy stands in its way.
        </p>
      )}
      {several && (
        <p className="muted set__note">
          Whoever runs this server sees and manages every domain on it. Anyone who needs their own
          admin should have their own server.
        </p>
      )}
      {form ? (
        <div className="domain-form">
          <input
            aria-label="Domain"
            placeholder="example.com"
            value={form.domain}
            onChange={(e) => setForm({ ...form, domain: e.target.value })}
          />
          <label className="set__row">
            <input
              type="radio"
              name="records"
              checked={form.records === "wildcard"}
              onChange={() => setForm({ ...form, records: "wildcard" })}
            />
            <span>Wildcard record (recommended)</span>
            <Hint label="About a wildcard record">
              At your DNS provider, add an A record for <code>*.{shown}</code> with the value{" "}
              <code>{address}</code>, once. Every app then works with no more DNS changes. A website
              at <code>{shown}</code> itself also needs a record for <code>@</code>.
            </Hint>
          </label>
          <label className="set__row">
            <input
              type="radio"
              name="records"
              checked={form.records === "each_name"}
              onChange={() => setForm({ ...form, records: "each_name" })}
            />
            <span>A record per app</span>
            <Hint label="About a record per app">
              Add an A record for each app, such as <code>app.{shown}</code> with the value{" "}
              <code>{address}</code>. Commitarium tells you which one when you deploy.
            </Hint>
          </label>
          {form.editing !== null && form.domain.trim() !== form.editing && (
            <p className="muted set__note">
              Apps here keep their names and move to the new domain; their old addresses stop
              working.
            </p>
          )}
          <div className="set__row">
            <button disabled={busy || !form.domain.trim()} onClick={save}>
              {busy ? "Checking…" : form.editing !== null ? "Save" : "Add"}
            </button>
            <button
              className="ghost"
              disabled={busy}
              onClick={() => {
                setForm(null);
                setError(null);
              }}
            >
              Cancel
            </button>
          </div>
        </div>
      ) : (
        <div className="set__row">
          <button
            disabled={busy}
            onClick={() => setForm({ domain: "", records: "wildcard", editing: null })}
          >
            Add domain
          </button>
        </div>
      )}
      {error && <p className="set__note">{error}</p>}
      <p className="muted set__note">
        Not sure how to set this up at your DNS provider?{" "}
        <AskMayka
          link
          onLeave={onLeave}
          message={
            `Help me point ${form?.domain.trim() ? `my domain ${shown}` : (first ?? "my domain")} ` +
            `at this server (${address}), with ` +
            `${(form?.records ?? domains[0]?.records ?? "wildcard") === "wildcard" ? "one wildcard record" : "a record per app"}.`
          }
        />{" "}
        to walk you through it.
      </p>
    </section>
  );
}

const NO_PROVIDER: ConnectSignIn = { issuer: "", client_id: "", client_secret: "" };

/** One block per domain, or, on the private network, one without. */
function PerDomain({
  children,
  privateToo,
}: {
  children: (domain: string | undefined, several: boolean) => React.ReactNode;
  /** Shown once on the private network too, for no domain. */
  privateToo?: boolean;
}) {
  const overview = usePublishing();
  if (!overview) return null;
  if (overview.reach === "private") return privateToo ? <>{children(undefined, false)}</> : null;
  const several = overview.domains.length > 1;
  return (
    <>
      {overview.domains.map((domain) => (
        <Fragment key={domain.name}>{children(domain.name, several)}</Fragment>
      ))}
    </>
  );
}

// Company sign-in for deployed apps (COMPANY_SIGN_IN_PLAN.md): a domain's
// provider, connected once for that domain (a cookie cannot cross domains).
// One authentik on the server can serve every domain. Nothing of it runs
// until then.
function SignInSection({
  domain,
  several,
  onLeave,
}: {
  domain: string;
  /** The server has more than one domain: each block says whose it is. */
  several: boolean;
  onLeave?: () => void;
}) {
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

  const entry = overview?.domains.find((item) => item.name === domain);
  if (!overview || !entry || overview.reach === "private") return null;
  const provider = entry.sign_in;
  const authentik = overview.authentik;
  const memory = authentik.memory_total_bytes;
  const memoryGb = memory === null ? null : Math.round(memory / 1024 ** 3);
  const isFirst = overview.domains[0]?.name === domain;
  const home = authentikHome(authentik.admin_url, overview.domains);
  // Once two domains use authentik, each lets in only its own group.
  const authentikDomains = overview.domains.filter((item) => item.authentik).length;
  const install = () => {
    setInstalling(true);
    void run(() => installAuthentik(domain)).finally(() => setInstalling(false));
  };
  const redirect = provider?.redirect_url ?? `https://sign-in.${domain}/oauth2/callback`;
  const group = (
    <p className="muted set__note">
      Only people in the <strong>{domain} people</strong> group in authentik&apos;s admin can sign
      in at {domain}. Add them there yourself: people and passwords stay out of the chat.
    </p>
  );
  const signOut = provider?.sign_out_url && (
    <p className="muted set__note">
      Sign out of every app: <code>{provider.sign_out_url}</code>
      <Hint label="About signing out">
        Ends the company sign-in session for every app at {domain}. The apps page links it; ask
        Mayka to add a Sign out link to an app.
      </Hint>
    </p>
  );
  const field = (key: keyof ConnectSignIn) => ({
    value: asked[key] ?? "",
    autoCapitalize: "off",
    autoCorrect: "off",
    spellCheck: false,
    onChange: (e: React.ChangeEvent<HTMLInputElement>) =>
      setAsked((current) => ({ ...current, [key]: e.target.value })),
  });
  const connect = () =>
    void run(() => connectSignIn({ ...asked, domain })).then((done) => {
      if (!done) return;
      setEditing(false);
      setAsked(NO_PROVIDER);
    });

  return (
    <section className="set__section">
      <h3 className="set__heading">
        {several ? `Company sign-in · ${domain}` : "Company sign-in"}
      </h3>
      {provider && entry.authentik ? (
        <>
          <p className="set__note">
            Apps can let people sign in with their company account, through authentik on this server
            {authentik.running ? "" : " (starting)"}. Add people and groups in its admin page.
          </p>
          {authentikDomains > 1 && group}
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
          {signOut}
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
              title={
                authentikDomains > 1
                  ? `Stops sign-in at ${domain}; authentik keeps serving the other domains`
                  : "Stops authentik; its people and groups stay for setting it up again"
              }
              onClick={() => void run(() => disconnectSignIn(domain))}
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
          {signOut}
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
            <button
              className="ghost"
              disabled={busy}
              onClick={() => void run(() => disconnectSignIn(domain))}
            >
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
                  `Help me set up company sign-in for this server, at ${domain}.`,
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
          {!provider && authentik.installed && (
            <>
              <h4 className="set__heading">No provider? Use this server&apos;s authentik</h4>
              <p className="muted set__note">
                authentik already runs on this server{home ? `, at auth.${home}` : ""}. It can sign
                people in at {domain} too, with its own application in authentik.
              </p>
              {group}
              <button disabled={busy} onClick={install}>
                {installing ? "Connecting authentik…" : "Use this server's authentik"}
              </button>
            </>
          )}
          {!provider && !authentik.installed && isFirst && (
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
                {several && " Your other domains can use it too, once it runs."}
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
    overview.domains.length === 0 &&
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
            company sign-in. Set it {where("Domains")}: it explains the DNS record to add.
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
// Set when Homelab is turned on here: once the server is ready, this
// computer moves to its Tailscale address by itself. It outlives the dialog,
// since signing in to Tailscale happens in a browser.
let connectWhenReady = false;

/** Settings → Server → Homelab: one button each way, each asked first. */
export function HomelabSettings({ onLeave }: { onLeave?: () => void }) {
  const [overview, setOverview] = useState<PublishingOverview | null>(null);
  const [confirming, setConfirming] = useState(false);
  // Turning off would cut this computer off: why, and the way back it tried.
  const [stranded, setStranded] = useState<{ tried: string | null } | null>(null);
  // Turned off anyway: this computer can no longer reach the server.
  const [lost, setLost] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const refresh = useCallback(() => {
    void readPublishing()
      .then(setOverview)
      .catch((e) => setError(String(e)));
  }, []);
  useFresh(refresh);
  const on = overview?.reach === "private";
  const tailscale = overview?.tailscale ?? null;
  const ready = on && tailscale?.state === "ready";
  const waiting = on && !ready;
  // Signing in and turning on HTTPS happen elsewhere: look again while waiting.
  useEffect(() => {
    if (!waiting) return;
    const timer = setInterval(refresh, 4000);
    return () => clearInterval(timer);
  }, [waiting, refresh]);

  const server = connectedServer();
  const address = tailscale?.address ?? null;
  const onTailscale = Boolean(address && server?.startsWith(address + ":"));

  const connect = useCallback(async () => {
    if (!address) return;
    connectWhenReady = false;
    setBusy(true);
    setError(null);
    try {
      const found = await readPlaces();
      if (!found.active) throw new Error("This computer is not showing a server.");
      await moveServer(found.active, `${address}:7443`);
      // The window shows the server from its new address, as on a switch.
      window.location.reload();
    } catch (e) {
      setError(String(e));
      setBusy(false);
    }
  }, [address]);
  useEffect(() => {
    if (ready && !onTailscale && connectWhenReady) void connect();
  }, [ready, onTailscale, connect]);

  if (!overview) {
    // Not read: say why, rather than an empty tab.
    return error ? (
      <section className="set__section">
        <h3 className="set__heading">Homelab</h3>
        <p className="set__note">Commitarium could not read the server&apos;s settings: {error}</p>
        {throughTailscale(server) && <p className="muted set__note">{TAILSCALE_HINT}</p>}
      </section>
    ) : null;
  }

  const turnOn = async () => {
    setBusy(true);
    setError(null);
    try {
      await setReach("private");
      connectWhenReady = true;
      setConfirming(false);
      publishingChanged();
      refresh();
    } catch (e) {
      setError(String(e));
    } finally {
      setBusy(false);
    }
  };
  const turnOff = async (anyway = false) => {
    setBusy(true);
    setError(null);
    try {
      // Off stops the server's Tailscale: a computer that reaches the server
      // through it goes back to the address it had first, or is told what
      // losing the server means before it does.
      if (onTailscale && !anyway) {
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
      }
      connectWhenReady = false;
      if (onTailscale && anyway) {
        // The answer would come back through Tailscale, which this stops: it
        // may never arrive, so the server is taken as switched either way.
        await Promise.race([
          setReach("internet").catch(() => undefined),
          new Promise((done) => setTimeout(done, 8000)),
        ]);
        setLost(true);
        return;
      }
      await setReach("internet");
      // The window shows the server from its new address, as on a switch.
      if (onTailscale) {
        window.location.reload();
        return;
      }
      setConfirming(false);
      setStranded(null);
      publishingChanged();
      refresh();
    } catch (e) {
      setError(String(e));
    } finally {
      setBusy(false);
    }
  };
  const cancel = () => {
    setConfirming(false);
    setStranded(null);
  };
  const oldLinks = overview.apps.length > 0 ? " Anyone with an old link needs the new one." : "";

  if (lost) {
    return (
      <section className="set__section">
        <h3 className="set__heading">Homelab</h3>
        <div className="set__warning" role="alert">
          <p className="set__note">
            <strong>Homelab is off, and this computer can no longer reach the server.</strong> To
            connect again, on the same network as the server, run{" "}
            <code>sudo commitarium-host pair</code> on it and paste the line it prints into
            Commitarium, under Connect a server.
          </p>
          <div className="set__row">
            <button onClick={() => void showPlace(null)}>Show this computer</button>
          </div>
        </div>
      </section>
    );
  }

  return (
    <section className="set__section">
      <h3 className="set__heading">Homelab</h3>
      <p className="muted set__note">
        For a computer at home: your apps go on a private network that only your own devices reach,
        through Tailscale, with no domain and nothing open on your router. This computer connects to
        the server through it too, so Commitarium works away from home. Off, your apps are served at
        your own domain, for anyone on the internet.
      </p>
      {on && (
        <TailscaleSteps
          tailscale={tailscale}
          busy={busy}
          onTailscale={onTailscale}
          onMove={() => void connect()}
        />
      )}
      {!confirming && (
        <div className="set__row">
          <button
            className={on ? "ghost" : undefined}
            disabled={busy}
            onClick={() => setConfirming(true)}
          >
            {on ? "Turn off Homelab" : "Turn on Homelab"}
          </button>
        </div>
      )}
      {confirming && !stranded && (
        <div className="set__warning" role="alertdialog" aria-label="Confirm Homelab change">
          <p className="set__note">
            <strong>{on ? "Turn off Homelab?" : "Turn on Homelab?"}</strong>{" "}
            {on
              ? `Your apps move to addresses under your own domain, for anyone on the internet. Every app gets a new address.${oldLinks}`
              : `Your apps move to a private network that only your devices reach, through Tailscale. Every app gets a new address, and company sign-in is not offered there.${oldLinks} Next you sign in to Tailscale; once the server is ready, this computer connects through it by itself.`}
            {on &&
              onTailscale &&
              " This computer first goes back to reaching the server at its home network address."}
          </p>
          <div className="set__row">
            <button disabled={busy} onClick={() => void (on ? turnOff() : turnOn())}>
              {busy ? "Switching…" : on ? "Turn off" : "Turn on"}
            </button>
            <button className="ghost" disabled={busy} onClick={cancel}>
              Cancel
            </button>
          </div>
        </div>
      )}
      {confirming && stranded && (
        <div
          className="set__warning"
          role="alertdialog"
          aria-label="This computer would lose the server"
        >
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
            <button className="ghost" disabled={busy} onClick={() => void turnOff(true)}>
              Turn off anyway
            </button>
            <button className="ghost" disabled={busy} onClick={cancel}>
              Cancel
            </button>
          </div>
        </div>
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
                {busy ? "Connecting…" : "Connect through Tailscale"}
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

// The apps page (APPS_PAGE_PLAN.md): apps.<domain> lists the apps at that
// domain and opens them, for the people who use them; one per domain, or on
// the private network one at the Tailscale name. Off until turned on.
function AppsPageSection({ domain, several }: { domain?: string; several: boolean }) {
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
        const entry = found.domains.find((item) => item.name === domain);
        const page = domain ? (entry?.apps_page ?? null) : found.apps_page;
        setName(page?.name ?? "apps");
        setAccess(page?.access ?? (entry?.sign_in ? "company" : "private"));
      })
      .catch((e) => setError(String(e)));
  }, [domain]);
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
  const entry = overview?.domains.find((item) => item.name === domain);
  if (!overview || (!entry && !onTailnet)) return null;
  const page = onTailnet ? overview.apps_page : (entry?.apps_page ?? null);
  // Its page lists only the apps whose main address is at its domain.
  const listed = onTailnet ? overview.apps : overview.apps.filter((app) => app.domain === domain);
  const shownName = name.trim().toLowerCase() || "apps";
  // On the private network the page has the Tailscale name to itself.
  const renamed = !onTailnet && page !== null && shownName !== page.name;
  const choices = ACCESS_CHOICES.filter(
    (choice) =>
      choice.value !== "company" || (!onTailnet && (entry?.sign_in || page?.access === "company")),
  );
  // A password page needs one the first time; after that it keeps its own.
  const choose = (next: Access) => {
    setAccess(next);
    if (!page) return;
    if (next === "private") {
      // It keeps the password it had; without one, ask for it.
      void setAppsPage("private", undefined, undefined, domain)
        .then(refresh)
        .catch(() => setAskPassword(true));
      return;
    }
    void run(() => setAppsPage(next, undefined, undefined, domain));
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
          onClick={() => void run(() => setAppsPage("private", password, undefined, domain))}
        >
          Save
        </button>
      )}
    </div>
  );

  return (
    <section className="set__section">
      <h3 className="set__heading">{several ? `Apps page · ${domain}` : "Apps page"}</h3>
      <p className="muted set__note">
        One address that lists {several ? `the apps at ${domain}` : "this server's apps"} and opens
        them, in any browser and on a phone. People use the apps there; nothing of Commitarium is
        shown.
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
          <span className="muted">.{domain}</span>
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
              onClick={() => void run(() => setAppsPage(page.access, undefined, name, domain))}
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
                ? void revealAppsPagePassword(domain).then(setShownPassword)
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
      {page && listed.length > 0 && (
        <>
          <h4 className="set__heading">Listed there</h4>
          {listed.map((app) => (
            <label key={app.project_id} className="set__row">
              <input
                type="checkbox"
                checked={app.listed}
                disabled={busy}
                onChange={(e) => void run(() => setListed(app.project_id, e.target.checked))}
              />
              <span>
                {app.name || app.url.replace("https://", "")}
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
            onClick={() => void run(() => setAppsPage(null, undefined, undefined, domain))}
          >
            Turn off
          </button>
        ) : (
          <button
            disabled={busy || !name.trim() || (access === "private" && !password)}
            onClick={() => void run(() => setAppsPage(access, password, name, domain))}
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
  const changed = () => {
    refresh();
    window.dispatchEvent(new Event(PLACES_CHANGED));
  };
  const act = (work: Promise<unknown>) => work.then(changed).catch((e) => setError(String(e)));
  const save = (fingerprint: string) => {
    setNaming(null);
    act(renameServer(fingerprint, name));
  };
  const forget = async (fingerprint: string, label: string) => {
    const sure = await confirmAction(
      `Forget ${label} on this computer? It keeps running, but this computer can only reach it again by pairing anew, with a code from the server.`,
      "Forget",
    );
    if (!sure) return;
    const shown = known.active === fingerprint;
    void forgetServer(fingerprint)
      .then(() => (shown ? window.location.reload() : changed()))
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
          <button className="ghost" onClick={() => void forget(server.fingerprint, server.name)}>
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
      <DomainSection onLeave={onLeave} />
      <PerDomain>
        {(domain, several) =>
          domain && <SignInSection domain={domain} several={several} onLeave={onLeave} />
        }
      </PerDomain>
      <PerDomain privateToo>
        {(domain, several) => <AppsPageSection domain={domain} several={several} />}
      </PerDomain>

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
