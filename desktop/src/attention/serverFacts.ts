import { connectedServer } from "../connection";
import {
  publishing,
  type Access,
  type DomainShown,
  type PublishedApp,
  type PublishingOverview,
} from "../ipc";
import { authentikHome } from "../views/domainRecords";

// What the server reports about itself, in short lines for Mayka with every
// message while the app is connected to a server, so she reads it instead of
// asking (the coordinator cannot reach the host). Nothing here is a secret:
// the host never shows one.

const ACCESS: Record<Access, string> = {
  public: "open to anyone",
  private: "behind a password",
  company: "behind company sign-in",
};

const gb = (bytes: number) => `${(bytes / 1024 ** 3).toFixed(1)} GB`;

const host = (url: string) => url.replace(/^https:\/\//, "").replace(/\/$/, "");

/** Whether a domain's records check out, from its apps' addresses. */
function dnsChecked(domain: DomainShown, apps: PublishedApp[]): string {
  const here = apps.filter((app) => app.domain === domain.name);
  if (here.length === 0) return "not checked yet (no app there)";
  return here.some((app) => app.record_problem)
    ? "a record is missing (see the apps)"
    : "its records point here";
}

/** A domain in one line: its DNS, its company sign-in and its apps page. */
function describeDomain(domain: DomainShown, apps: PublishedApp[]): string {
  const dns = domain.records === "wildcard" ? "one wildcard record" : "a record for each address";
  const signIn = domain.sign_in
    ? domain.authentik
      ? "authentik on this server"
      : `connected (issuer ${domain.sign_in.issuer})`
    : "not connected";
  const page = domain.apps_page
    ? `on, ${ACCESS[domain.apps_page.access]}, at ${host(domain.apps_page.url)}`
    : "off";
  return `Domain: ${domain.name}, DNS as ${dns}, ${dnsChecked(domain, apps)}; company sign-in ${signIn}; apps page ${page}`;
}

/** An app by its addresses, main first: "site.is (and www) + site.com",
 * marked when it is the project the chat is in. */
function describeApp(app: PublishedApp, project?: string): string {
  const urls = app.urls.length > 0 ? app.urls : app.url ? [app.url] : [];
  const where = urls.map(host).join(" + ") || app.name;
  const www = app.www && (app.name === "" || app.aliases.some((alias) => alias.name === ""));
  const groups = app.access === "company" && app.groups.length > 0;
  return `${where}${www ? " (and www)" : ""} (${ACCESS[app.access]}${
    groups ? `, groups ${app.groups.join(", ")}` : ""
  }${app.live ? "" : ", stopped"}${app.record_problem ? `, DNS: ${app.record_problem}` : ""}${
    app.project_id === project ? ", this project's app" : ""
  })`;
}

export function describeServer(o: PublishingOverview, project?: string): string {
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
    for (const domain of o.domains) lines.push(describeDomain(domain, o.apps));
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
      : `Deployed apps: ${o.apps.map((app) => describeApp(app, project)).join(", ")}`,
  );
  return lines.join("\n");
}

/** The shown server's facts, with project's app marked, or undefined on
 * this computer or when the server does not answer: the chat goes on
 * either way. */
export async function serverFacts(project?: string): Promise<string | undefined> {
  if (!connectedServer()) return undefined;
  try {
    return describeServer(await publishing(), project);
  } catch {
    return undefined;
  }
}
