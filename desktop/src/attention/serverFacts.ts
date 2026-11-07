import { connectedServer } from "../connection";
import { publishing, type Access, type PublishingOverview } from "../ipc";

// What the server reports about itself, in short lines for Mayka when a chat
// opens on serving apps or company sign-in, so she reads it instead of asking
// (the coordinator cannot reach the host). Nothing here is a secret: the host
// never shows one.

const ACCESS: Record<Access, string> = {
  public: "open to anyone",
  private: "behind a password",
  company: "behind company sign-in",
};

const gb = (bytes: number) => `${(bytes / 1024 ** 3).toFixed(1)} GB`;

export function describeServer(o: PublishingOverview): string {
  const lines: string[] = [];
  if (o.placement) {
    const where = { home: "at home", cloud: "in the cloud", other: "somewhere else" }[o.placement];
    lines.push(`Where the owner said it runs: ${where}`);
  }
  if (o.reach === "private") {
    const tailscale = o.tailscale;
    const state = tailscale ? tailscale.state.replace(/_/g, " ") : "starting";
    lines.push(
      `Homelab: on (Tailscale ${state}${tailscale?.own ? ", the server's own Tailscale" : ""}${
        tailscale?.name ? `, named ${tailscale.name}` : ""
      })`,
    );
  } else {
    lines.push("Homelab: off");
    lines.push(
      o.domain
        ? `Domain: ${o.domain}, DNS as ${o.records === "wildcard" ? "one wildcard record" : "a record for each address"}`
        : "Domain: not set yet",
    );
  }
  if (o.server_address) lines.push(`Public IP address: ${o.server_address}`);
  const memory = o.authentik.memory_total_bytes;
  if (memory) lines.push(`Memory: ${gb(memory)}`);
  if (o.sign_in) lines.push(`Company sign-in: connected (issuer ${o.sign_in.issuer})`);
  else lines.push("Company sign-in: not connected");
  if (o.authentik.installed) {
    lines.push(`authentik: set up on this server, ${o.authentik.running ? "running" : "stopped"}`);
  }
  lines.push(
    o.apps_page
      ? `Apps page: on, ${ACCESS[o.apps_page.access]}, at ${o.apps_page.url}`
      : "Apps page: off",
  );
  if (o.apps.length === 0) lines.push("Deployed apps: none");
  else {
    const apps = o.apps.map(
      (app) => `${app.name} (${ACCESS[app.access]}${app.live ? "" : ", stopped"})`,
    );
    lines.push(`Deployed apps: ${apps.join(", ")}`);
  }
  return lines.join("\n");
}

/** The shown server's facts, or undefined on this computer or when the
 * server does not answer: the chat opens either way. */
export async function serverFacts(): Promise<string | undefined> {
  if (!connectedServer()) return undefined;
  try {
    return describeServer(await publishing());
  } catch {
    return undefined;
  }
}
