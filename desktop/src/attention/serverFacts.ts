import { connectedServer } from "../connection";
import {
  publishing,
  type Access,
  type DomainShown,
  type PublishedApp,
  type PublishingOverview,
} from "../ipc";
import { authentikHome } from "../views/domainRecords";

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

const host = (url: string) => url.replace(/^https:\/\//, "").replace(/\/$/, "");

/** A domain in one line: its DNS, its company sign-in and its apps page. */
function describeDomain(domain: DomainShown): string {
  const dns = domain.records === "wildcard" ? "one wildcard record" : "a record for each address";
  const signIn = domain.sign_in
    ? domain.authentik
      ? "authentik on this server"
      : `connected (issuer ${domain.sign_in.issuer})`
    : "not connected";
  const page = domain.apps_page
    ? `on, ${ACCESS[domain.apps_page.access]}, at ${host(domain.apps_page.url)}`
    : "off";
  return `Domain: ${domain.name}, DNS as ${dns}; company sign-in ${signIn}; apps page ${page}`;
}

/** An app by its addresses, main first: "site.is (and www) + site.com". */
function describeApp(app: PublishedApp): string {
  const urls = app.urls.length > 0 ? app.urls : app.url ? [app.url] : [];
  const where = urls.map(host).join(" + ") || app.name;
  const www = app.www && (app.name === "" || app.aliases.some((alias) => alias.name === ""));
  const groups = app.access === "company" && app.groups.length > 0;
  return `${where}${www ? " (and www)" : ""} (${ACCESS[app.access]}${
    groups ? `, groups ${app.groups.join(", ")}` : ""
  }${app.live ? "" : ", stopped"})`;
}

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
    if (o.domains.length === 0) lines.push("Domain: not set yet");
    for (const domain of o.domains) lines.push(describeDomain(domain));
  }
  if (o.server_address) lines.push(`Public IP address: ${o.server_address}`);
  const memory = o.authentik.memory_total_bytes;
  if (memory) lines.push(`Memory: ${gb(memory)}`);
  if (o.authentik.installed) {
    const home = authentikHome(o.authentik.admin_url, o.domains);
    lines.push(
      `authentik: set up on this server${home ? ` at auth.${home}` : ""}, ${
        o.authentik.running ? "running" : "stopped"
      }`,
    );
  }
  if (o.reach === "private") {
    lines.push(
      o.apps_page
        ? `Apps page: on, ${ACCESS[o.apps_page.access]}, at ${host(o.apps_page.url)}`
        : "Apps page: off",
    );
  }
  lines.push(
    o.apps.length === 0
      ? "Deployed apps: none"
      : `Deployed apps: ${o.apps.map(describeApp).join(", ")}`,
  );
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
