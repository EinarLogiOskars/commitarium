import { connectedServer } from "../connection";
import { moveServer, places as readPlaces, type PublishingOverview } from "../ipc";

// Turning Homelab on or off (HOMELAB_PLAN.md), as Settings → Server →
// Homelab and Mayka's set_homelab card both ask it: the same warning, and
// the same way back for a computer that reaches the server through
// Tailscale, which turning it off stops.

/** Whether this computer reaches the server at its Tailscale address. */
export function onTailscaleNow(overview: PublishingOverview): boolean {
  const address = overview.tailscale?.address ?? null;
  return Boolean(address && connectedServer()?.startsWith(address + ":"));
}

/** What turning Homelab on or off changes, asked before anything happens. */
export function HomelabWarning({
  on,
  apps,
  onTailscale,
}: {
  /** Whether Homelab is on now, so the change turns it off. */
  on: boolean;
  /** How many apps have an address, each of which changes. */
  apps: number;
  onTailscale: boolean;
}) {
  const oldLinks = apps > 0 ? " Anyone with an old link needs the new one." : "";
  return (
    <>
      <strong>{on ? "Turn off Homelab?" : "Turn on Homelab?"}</strong>{" "}
      {on
        ? `Your apps move to addresses under your own domain, for anyone on the internet. Every app gets a new address.${oldLinks}`
        : `Your apps move to a private network that only your devices reach, through Tailscale. Every app gets a new address, and company sign-in is not offered there.${oldLinks} Next you sign in to Tailscale; once the server is ready, this computer connects through it by itself.`}
      {on &&
        onTailscale &&
        " This computer first goes back to reaching the server at its home network address."}
    </>
  );
}

/** Why turning Homelab off would cut this computer off the server. */
export function StrandedWarning({ tried }: { tried: string | null }) {
  return (
    <>
      This computer reaches the server only through Tailscale
      {tried ? `, and it could not reach it at its home network address (${tried}) just now` : ""}.
      Turning Homelab off stops Tailscale on the server, so this computer loses the server. To
      connect again you would need to be on the same network as the server, run{" "}
      <code>sudo commitarium-host pair</code> on it, and paste the line it prints into Commitarium.
      {tried && " If you are away from home, turning it off from home avoids this."}
    </>
  );
}

/**
 * Before Homelab goes off, moves this computer back to the server's home
 * network address. Null once it is there; otherwise the address it tried,
 * or null inside when there is none, so the caller warns first.
 */
export async function moveBackFirst(): Promise<{ tried: string | null } | null> {
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
  return movedBack ? null : { tried: back };
}

/** How long turning off waits for an answer that comes back through
 * Tailscale, which it stops: it may never arrive. */
export const LOSING_ANSWER_MS = 8000;
