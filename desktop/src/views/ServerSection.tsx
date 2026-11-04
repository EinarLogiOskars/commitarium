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
  setReach,
  moveServer,
  revealAppsPagePassword,
  setAppsPage,
  setListed,
  type Access,
  type Reach,
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

/** This app's API version: raise it with `API_VERSION` in the host. */
const API_VERSION = 1;

function versionProblem(api: number): string | null {
  if (api === API_VERSION) return null;
  return api < API_VERSION
    ? "Your server is too old for this app. Update the server."
    : "This app is too old for your server. Update the app.";
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
// (PUBLISHING_PLAN.md). It is accepted once its wildcard record points here.
function DomainSection() {
  const [overview, setOverview] = useState<PublishingOverview | null>(null);
  const [domain, setDomainText] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const refresh = useCallback(() => {
    void readPublishing()
      .then((found) => {
        setOverview(found);
        setDomainText(found.domain ?? "");
      })
      .catch((e) => setError(String(e)));
  }, []);
  useEffect(refresh, [refresh]);

  const save = async () => {
    setBusy(true);
    setError(null);
    try {
      await setDomain(domain);
      refresh();
    } catch (e) {
      setError(String(e));
    } finally {
      setBusy(false);
    }
  };

  const shown = domain.trim().replace(/^\*\./, "") || "example.com";
  // On a private network there is no domain: the Tailscale name stands in.
  if (overview?.reach === "private") return null;
  return (
    <section className="set__section">
      <h3 className="set__heading">Domain</h3>
      <p className="muted set__note">
        Deployed apps get an address under this domain, such as app.{shown}. At your DNS provider,
        add an A record for <code>*.{shown}</code> with the value{" "}
        <code>{overview?.server_address ?? "this server's address"}</code>. Names that already have
        their own records stay where they are.
      </p>
      <div className="set__row">
        <input
          aria-label="Domain"
          placeholder="example.com"
          value={domain}
          onChange={(e) => setDomainText(e.target.value)}
        />
        <button disabled={busy || !domain.trim()} onClick={() => void save()}>
          {busy ? "Checking…" : overview?.domain ? "Change" : "Use this domain"}
        </button>
      </div>
      {error && <p className="set__note">{error}</p>}
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
  useEffect(refresh, [refresh]);

  const run = async (work: () => Promise<unknown>) => {
    setBusy(true);
    setError(null);
    try {
      await work();
      refresh();
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

// Who reaches the apps (HOMELAB_PLAN.md): anyone on the internet at the
// server's domain, or only the owner's devices, on their Tailscale network,
// with the steps Tailscale takes.
function ReachSection() {
  const [overview, setOverview] = useState<PublishingOverview | null>(null);
  const [asked, setAsked] = useState<Reach | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [moved, setMoved] = useState(false);

  const refresh = useCallback(() => {
    void readPublishing()
      .then(setOverview)
      .catch((e) => setError(String(e)));
  }, []);
  useEffect(refresh, [refresh]);
  const waiting = overview?.reach === "private" && overview.tailscale?.state !== "ready";
  // Signing in and turning on HTTPS happen elsewhere: look again while waiting.
  useEffect(() => {
    if (!waiting) return;
    const timer = setInterval(refresh, 4000);
    return () => clearInterval(timer);
  }, [waiting, refresh]);

  if (!overview) return null;
  const reach = overview.reach;
  const tailscale = overview.tailscale;
  const switching = asked !== null && asked !== reach;
  const change = async () => {
    if (!asked) return;
    setBusy(true);
    setError(null);
    try {
      await setReach(asked);
      setAsked(null);
      refresh();
    } catch (e) {
      setError(String(e));
    } finally {
      setBusy(false);
    }
  };
  const server = connectedServer();
  const onTailscale = tailscale?.address && server?.startsWith(tailscale.address + ":");
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
      <h3 className="set__heading">Who reaches your apps</h3>
      <label className="muted deploy-access">
        <select
          value={asked ?? reach}
          disabled={busy}
          onChange={(e) => setAsked(e.target.value as Reach)}
        >
          <option value="internet">Anyone on the internet, at your own domain</option>
          <option value="private">Only my devices, on a private network</option>
        </select>
      </label>
      {switching && (
        <>
          <p className="set__note">
            {asked === "private"
              ? "Your apps move to a private network that only your devices reach, through Tailscale: no domain, nothing open on your router. Every app gets a new address, and company sign-in is not offered there."
              : "Your apps move to addresses under your own domain, for anyone on the internet. Every app gets a new address."}
            {overview.apps.length > 0 && " Anyone with an old link needs the new one."}
          </p>
          <div className="set__row">
            <button disabled={busy} onClick={() => void change()}>
              {busy ? "Switching…" : "Switch"}
            </button>
            <button className="ghost" disabled={busy} onClick={() => setAsked(null)}>
              Cancel
            </button>
          </div>
        </>
      )}
      {!switching && reach === "private" && (
        <TailscaleSteps
          tailscale={tailscale}
          busy={busy}
          onTailscale={Boolean(onTailscale) || moved}
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
  useEffect(refresh, [refresh]);

  const run = async (work: () => Promise<unknown>) => {
    setBusy(true);
    setError(null);
    try {
      await work();
      setPassword("");
      setAskPassword(false);
      refresh();
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

      <ReachSection />
      <DomainSection />
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
