import { useCallback, useEffect, useState } from "react";
import { connectedServer } from "../connection";
import {
  hostUpdate,
  hostVersion,
  installHostUpdate,
  openPairing,
  pairedDevices,
  places as readPlaces,
  publishing as readPublishing,
  setDomain,
  type PublishingOverview,
  forgetServer,
  renameServer,
  revokeDevice,
  type HostUpdate,
  type PairedDevice,
  type Places,
} from "../ipc";
import { showPlace } from "./PlaceSwitch";
import { ConnectForm } from "./StartChoice";

/** This app's API version: raise it with `API_VERSION` in the host. */
const API_VERSION = 1;

function versionProblem(api: number): string | null {
  if (api === API_VERSION) return null;
  return api < API_VERSION
    ? "Your server is too old for this app. Update the server."
    : "This app is too old for your server. Update the app.";
}

// The servers this device knows, and the shown one's state, in Settings.
export function ServerSection({ running }: { running: boolean }) {
  const server = connectedServer();
  return (
    <>
      {server && <ConnectedServer server={server} running={running} />}
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

function ConnectedServer({ server, running }: { server: string; running: boolean }) {
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
