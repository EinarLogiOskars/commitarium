// Which server the window shows, if any (ADR-040). Set once before the app
// shows: switching reloads the window.
let server: string | null = null;

export const setServer = (address: string | null) => {
  server = address;
};

/** The paired server's address, or null when this computer is the host. */
export const connectedServer = () => server;

/** Whether this computer reaches the server at a Tailscale address
 * (100.64.0.0/10), as in Homelab mode. */
export const throughTailscale = (address: string | null = server): boolean => {
  const [a, b] = (address ?? "").split(".").map(Number);
  return a === 100 && b >= 64 && b < 128;
};

/** What to check when a server reached through Tailscale does not answer. */
export const TAILSCALE_HINT =
  "This computer reaches it through Tailscale: make sure Tailscale is on here, and on the server (sudo tailscale up there).";
