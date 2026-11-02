import { useEffect, useState } from "react";
import { chooseThisComputer, places as readPlaces, showServer, type Places } from "../ipc";
import { markSwitched } from "../boot/useBoot";
import { ConnectForm } from "./StartChoice";

const THIS_COMPUTER = "this-computer";
const CONNECT = "connect";

/** Moves the window to another side. Everything else follows a reload. */
export const showPlace = async (fingerprint: string | null) => {
  await (fingerprint ? showServer(fingerprint) : chooseThisComputer());
  markSwitched();
  window.location.reload();
};

/** The shown side's name: "This computer", or the server's name. */
export function usePlaceName(): string {
  const [name, setName] = useState("This computer");
  useEffect(() => {
    void readPlaces()
      .then((known) => {
        const shown = known.servers.find((server) => server.fingerprint === known.active);
        if (shown) setName(shown.name);
      })
      .catch(() => {});
  }, []);
  return name;
}

function LaptopIcon() {
  return (
    <svg width="14" height="14" viewBox="0 0 16 16" aria-hidden>
      <rect
        x="3"
        y="3"
        width="10"
        height="7"
        rx="1"
        fill="none"
        stroke="currentColor"
        strokeWidth="1.4"
      />
      <path d="M1.5 12.5h13" stroke="currentColor" strokeWidth="1.4" strokeLinecap="round" />
    </svg>
  );
}

function ServerIcon() {
  return (
    <svg width="14" height="14" viewBox="0 0 16 16" aria-hidden>
      <rect
        x="2.5"
        y="2.5"
        width="11"
        height="4.5"
        rx="1"
        fill="none"
        stroke="currentColor"
        strokeWidth="1.4"
      />
      <rect
        x="2.5"
        y="9"
        width="11"
        height="4.5"
        rx="1"
        fill="none"
        stroke="currentColor"
        strokeWidth="1.4"
      />
      <path
        d="M5 4.75h.01M5 11.25h.01"
        stroke="currentColor"
        strokeWidth="2"
        strokeLinecap="round"
      />
    </svg>
  );
}

// Which side the window shows: this computer or one of the paired servers
// (ADR-040). Each keeps working while another is shown.
export function PlaceSwitch() {
  const [known, setKnown] = useState<Places | null>(null);
  const [connecting, setConnecting] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    void readPlaces()
      .then(setKnown)
      .catch(() => setKnown({ servers: [], active: null }));
  }, []);

  if (!known) return null;
  const choose = (value: string) => {
    if (value === CONNECT) {
      setConnecting(true);
      return;
    }
    setBusy(true);
    setError(null);
    showPlace(value === THIS_COMPUTER ? null : value).catch((e) => {
      setError(String(e));
      setBusy(false);
    });
  };

  return (
    <>
      <span className={`place-switch ${known.active ? "place-switch--server" : ""}`}>
        {known.active ? <ServerIcon /> : <LaptopIcon />}
        <select
          className="place-switch__select"
          aria-label="Where Commitarium runs"
          title={error ?? undefined}
          value={known.active ?? THIS_COMPUTER}
          disabled={busy}
          onChange={(e) => choose(e.target.value)}
        >
          <option value={THIS_COMPUTER}>This computer</option>
          {known.servers.map((server) => (
            <option key={server.fingerprint} value={server.fingerprint}>
              {server.name}
            </option>
          ))}
          <option value={CONNECT}>Connect a server…</option>
        </select>
      </span>
      {error && <span className="place-switch__error">{error}</span>}
      {connecting && (
        <div className="modal" onClick={() => setConnecting(false)}>
          <div className="modal__card" onClick={(e) => e.stopPropagation()}>
            <div className="panel__head">
              <h2>Connect a server</h2>
              <button className="ghost" onClick={() => setConnecting(false)}>
                Close
              </button>
            </div>
            <ConnectForm />
          </div>
        </div>
      )}
    </>
  );
}
