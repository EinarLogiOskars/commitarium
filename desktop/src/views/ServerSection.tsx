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

  if (!overview?.domain) return null;
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

      <DomainSection />
      <SignInSection onLeave={onLeave} />

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
