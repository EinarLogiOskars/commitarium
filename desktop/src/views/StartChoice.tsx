import { useState } from "react";
import { connectToServer, restartApp, chooseThisComputer } from "../ipc";
import "./BootScreen.css";

export const INSTALL_COMMAND =
  "curl -fsSL https://github.com/EinarLogiOskars/commitarium/releases/latest/download/install-host.sh | sudo sh";

/** Pairs with a server from the line its install or `pair` command printed. */
export function ConnectForm() {
  const [line, setLine] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const connect = async () => {
    setBusy(true);
    setError(null);
    try {
      await connectToServer(line);
      await restartApp();
    } catch (e) {
      setError(String(e));
      setBusy(false);
    }
  };

  return (
    <>
      <p className="boot__message">Paste the line your server printed.</p>
      <input
        aria-label="Pairing line"
        placeholder="commitarium://…"
        value={line}
        onChange={(e) => setLine(e.target.value)}
      />
      {error && <p className="boot__detail boot__detail--error">{error}</p>}
      <div className="boot__actions">
        <button className="primary" disabled={busy || !line.trim()} onClick={() => void connect()}>
          {busy ? "Connecting…" : "Connect"}
        </button>
      </div>
      <p className="boot__detail">No server set up yet? Run this on it:</p>
      <code className="boot__detail">{INSTALL_COMMAND}</code>
    </>
  );
}

/** A first launch: where Commitarium runs. */
export function StartChoice({ onLocal }: { onLocal: () => void }) {
  const [connecting, setConnecting] = useState(false);

  return (
    <main className="boot">
      <div className="boot__band">
        <div className="boot__inner">
          <span className="boot__mark" aria-hidden />
          <h1 className="boot__title">Commitarium</h1>
          <div className="boot__status">
            {connecting ? (
              <ConnectForm />
            ) : (
              <>
                <p className="boot__message">Where should Commitarium run?</p>
                <div className="boot__actions">
                  <button
                    className="primary"
                    onClick={() => void chooseThisComputer().then(onLocal)}
                  >
                    Run on this computer
                  </button>
                  <button onClick={() => setConnecting(true)}>Connect to a server</button>
                </div>
              </>
            )}
          </div>
        </div>
      </div>
    </main>
  );
}
