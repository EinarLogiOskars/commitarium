import type { AppAddress, DomainShown, PublishedApp } from "../ipc";

// What a domain's DNS needs (MULTI_DOMAIN_PLAN.md, DNS), and which apps live
// on it, for Settings → Server and Mayka's facts.

/** An app's addresses, its main one first. */
export function addressesOf(app: PublishedApp): AppAddress[] {
  return [{ domain: app.domain, name: app.name }, ...app.aliases];
}

/** The apps with an address on a domain, those whose main address is there
 * first. */
export function appsOn(domain: string, apps: PublishedApp[]): PublishedApp[] {
  const here = apps.filter((app) => addressesOf(app).some((address) => address.domain === domain));
  return [
    ...here.filter((app) => app.domain === domain),
    ...here.filter((app) => app.domain !== domain),
  ];
}

/** An app's addresses on a domain, as links: its main one first. */
export function urlsOn(domain: string, app: PublishedApp): string[] {
  return [
    ...(app.domain === domain && app.url ? [app.url] : []),
    ...app.aliases.filter((alias) => alias.domain === domain).map((alias) => alias.url),
  ];
}

/**
 * The record names a domain needs at its DNS provider, each an A record for
 * the server's address: * for a wildcard, which does not cover the domain
 * itself, so @ for a website at the bare domain and www for its redirect;
 * with a record for each address, every name in use. authentikHome says
 * authentik lives at auth.<this domain>.
 */
export function recordsFor(
  domain: DomainShown,
  apps: PublishedApp[],
  authentikHome = false,
): string[] {
  const names = new Set<string>();
  const each = domain.records === "each_name";
  if (!each) names.add("*");
  for (const app of apps) {
    for (const address of addressesOf(app)) {
      if (address.domain !== domain.name) continue;
      if (address.name === "") {
        names.add("@");
        if (app.www) names.add("www");
      } else if (each) {
        names.add(address.name);
      }
    }
  }
  if (each) {
    if (domain.apps_page) names.add(domain.apps_page.name);
    if (domain.sign_in) names.add("sign-in");
    if (authentikHome) names.add("auth");
  }
  const first = ["*", "@", "www"].filter((name) => names.has(name));
  const rest = [...names].filter((name) => !first.includes(name)).sort();
  return [...first, ...rest];
}

/** The domain authentik lives at, from its admin page's address, or null
 * when it is not set up. */
export function authentikHome(adminUrl: string | null, domains: DomainShown[]): string | null {
  if (!adminUrl) return null;
  const host = adminUrl.replace(/^https?:\/\//, "").split(/[/:]/)[0];
  return domains.find((domain) => host === `auth.${domain.name}`)?.name ?? null;
}
