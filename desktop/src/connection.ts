// Which server this launch uses, if any (ADR-040). Set once before the app
// shows, because changing it restarts the app.
let server: string | null = null;

export const setServer = (address: string | null) => {
  server = address;
};

/** The paired server's address, or null when this computer is the host. */
export const connectedServer = () => server;
