import { useEffect, useRef, useState } from "react";
import {
  approveAssistantAction,
  dismissAssistantAction,
  type AssistantAction,
  type ChatTarget,
  type Conversation,
} from "../api/assistant";
import { serverFacts } from "../attention/serverFacts";
import { connectedServer } from "../connection";
import {
  addDomain,
  deploy,
  installAuthentik,
  publishApp,
  publishing,
  setAppAccess,
  setAppGroups,
  setAppsPage,
  setListed,
  setDomain,
  setReach,
  type Access,
  type PublishedApp,
  type PublishingOverview,
} from "../ipc";
import { hostOf } from "./DeployCard";
import {
  HomelabWarning,
  LOSING_ANSWER_MS,
  moveBackFirst,
  onTailscaleNow,
  StrandedWarning,
} from "./homelabSwitch";
import { connectWhenHomelabReady } from "./ServerSection";

// Mayka's cards that change the server (MULTI_DOMAIN_PLAN.md, M5): setting
// it up, deploying, and the apps page. The app applies each through the
// server's host, as Settings does, and only then records the approval, with
// what the host answered: that is the card's result, which Mayka reads, at
// once when it failed. A card the user asked for outright (asked) applies by
// itself, unless it removes, stops or hides something, turns Homelab, or
// moves an app's address. Nothing here takes a login or a password; those
// stay in Settings and at the user's providers.

/** The conversation outside any project. */
const TOP_LEVEL = "commitarium";

export const SERVER_CARDS = new Set<AssistantAction["type"]>([
  "add_domain",
  "install_authentik",
  "set_app_access",
  "set_homelab",
  "deploy_app",
  "set_apps_page",
  "list_on_apps_page",
]);

const ACCESS_WORDS: Record<Access, string> = {
  public: "open to anyone",
  private: "need its password",
  company: "open with company sign-in",
};

/** The question on an apps page card, by who may open it, and at which
 * domain when it names one. */
export function appsPageQuestion(how: string | undefined, domain?: string): string {
  const page = domain ? `the apps page at ${domain}` : "the apps page";
  switch (how) {
    case "off":
      return `Turn ${page} off?`;
    case "public":
      return `Turn on ${page}, open to anyone?`;
    case "company":
      return `Turn on ${page}, behind company sign-in?`;
    default:
      return `Turn on ${page}, behind its password?`;
  }
}

type CardAction = Pick<
  AssistantAction,
  "type" | "how" | "domain" | "name" | "aliases" | "app" | "groups"
>;

/** What a deploy_app card changes about an app already at an address: the
 * main address it moves from, and the addresses that stop working. */
export function deployChange(
  action: Pick<AssistantAction, "domain" | "name" | "aliases">,
  app: PublishedApp | undefined,
): { from: string | null; dropped: string[] } {
  if (!app || !action.domain || !app.domain) return { from: null, dropped: [] };
  const main = hostOf({ domain: action.domain, name: action.name ?? "" });
  const kept = new Set([main, ...(action.aliases ?? []).map(hostOf)]);
  const was = [hostOf(app), ...app.aliases.map(hostOf)];
  return {
    from: hostOf(app) === main ? null : hostOf(app),
    dropped: was.filter((address) => !kept.has(address)),
  };
}

/** A card's question and its button, in plain words. installed says
 * authentik already runs on the server; app is the project's deployed app,
 * for a deploy_app card. */
export function serverCardQuestion(
  action: CardAction,
  installed = false,
  app?: PublishedApp,
): [string, string] {
  switch (action.type) {
    case "deploy_app": {
      if (!action.domain) return ["Deploy the latest version, so visitors see it?", "Deploy"];
      const { from, dropped } = deployChange(action, app);
      const moving = from ? ` This moves it from ${from}.` : "";
      const gone = dropped.filter((address) => address !== from);
      const stops = gone.length
        ? ` ${gone.join(" and ")} stop${gone.length > 1 ? "" : "s"} working.`
        : "";
      return [`Deploy the latest version at ${deployAddress(action)}?${moving}${stops}`, "Deploy"];
    }
    case "set_apps_page":
      return [
        appsPageQuestion(action.how, action.domain),
        action.how === "off" ? "Turn off" : "Turn on",
      ];
    case "list_on_apps_page":
      return action.how === "hide"
        ? ["Hide this app on the apps page?", "Hide it"]
        : ["List this app on the apps page?", "List it"];
    case "add_domain":
      return [
        `Add ${action.domain} to your server, with ${
          action.how === "each_name" ? "a DNS record for each address" : "one wildcard DNS record"
        }?`,
        "Add domain",
      ];
    case "install_authentik":
      return installed
        ? [
            `Let ${action.domain || "your first domain"} sign people in with this server's authentik?`,
            "Connect",
          ]
        : [
            `Set up authentik on your server${action.domain ? ` for ${action.domain}` : ""}? It takes a few minutes.`,
            "Set up authentik",
          ];
    case "set_app_access": {
      const app = action.app || "this app";
      const groups = action.groups ?? [];
      const who =
        action.how === "company"
          ? `open with company sign-in, for ${groups.length ? groups.join(", ") : "anyone at the company"}`
          : ACCESS_WORDS[(action.how as Access) ?? "private"];
      return [`Make ${app} ${who}?`, "Change"];
    }
    case "set_homelab":
      return action.how === "off"
        ? ["Turn off Homelab?", "Turn off"]
        : ["Turn on Homelab?", "Turn on"];
    default:
      return ["", ""];
  }
}

/** The app a set_app_access card names: by its main address, or the
 * project's own app when it names none. */
export function findApp(
  apps: PublishedApp[],
  named: string | undefined,
  project: string | null,
): PublishedApp | undefined {
  const wanted = (named ?? "")
    .trim()
    .toLowerCase()
    .replace(/^https?:\/\//, "")
    .replace(/\/.*$/, "");
  if (!wanted) return apps.find((app) => app.project_id === project);
  return apps.find(
    (app) =>
      hostOf(app) === wanted ||
      app.aliases.some((alias) => hostOf(alias) === wanted) ||
      app.project_id === wanted,
  );
}

/** The address a deploy_app card proposes, with its extra addresses. */
export function deployAddress(action: Pick<AssistantAction, "domain" | "name" | "aliases">) {
  return [{ domain: action.domain ?? "", name: action.name ?? "" }, ...(action.aliases ?? [])]
    .map(hostOf)
    .join(" and ");
}

/** What the host answered, as the card's result. */
type Answer = { outcome: string; failed?: boolean };

const refused = (e: unknown): Answer => ({
  outcome: String(e).replace(/^Error:\s*/, ""),
  failed: true,
});

async function addTheDomain(action: AssistantAction): Promise<Answer> {
  const domain = action.domain ?? "";
  const records = action.how === "each_name" ? "each_name" : "wildcard";
  // The first domain is set as the one domain always was, so a server from
  // before several domains takes it too.
  const first = (await publishing()).domains.length === 0;
  await (first ? setDomain(domain, records) : addDomain(domain, records));
  return {
    outcome:
      records === "wildcard"
        ? `Added: *.${domain} points at this server.`
        : `Added: each address at ${domain} is checked when it is used.`,
  };
}

async function setUpAuthentik(action: AssistantAction): Promise<Answer> {
  await installAuthentik(action.domain || undefined);
  const after = await publishing();
  const domain = action.domain || after.domains[0]?.name || "";
  const shared = after.domains.filter((item) => item.authentik).length > 1;
  return {
    outcome:
      `authentik signs people in at ${domain}.` +
      (shared
        ? ` Only people in the "${domain} people" group in authentik's admin can sign in there; the user adds them.`
        : " The user adds people in authentik's admin."),
  };
}

async function changeAccess(action: AssistantAction, project: string | null): Promise<Answer> {
  const app = findApp((await publishing()).apps, action.app, project);
  if (!app) {
    return {
      outcome: `No app on the server has the address ${action.app || "of this project"}.`,
      failed: true,
    };
  }
  const access = (action.how as Access) ?? "private";
  try {
    await setAppAccess(app.project_id, access);
  } catch (e) {
    // Never had a password: it is chosen on the app's page, not here.
    if (access === "private") {
      return {
        outcome:
          "It needs a password first: the user chooses one on the app's Deployment page, under Who can open it.",
        failed: true,
      };
    }
    throw e;
  }
  if (access === "company") await setAppGroups(app.project_id, action.groups ?? []);
  return { outcome: `${hostOf(app)} is now ${ACCESS_WORDS[access]}.` };
}

/**
 * A deploy_app card: deploys the latest version, then, when Mayka proposed
 * an address, gives the app that address. A new app's access is the user's
 * to choose with its password, on its Deployment page; an app that has one
 * keeps it.
 */
async function deployIt(project: string | null, action: AssistantAction): Promise<Answer> {
  if (!project) return { outcome: "Deploying needs a project's chat.", failed: true };
  await deploy(project);
  const app = (await publishing()).apps.find((found) => found.project_id === project);
  if (!action.domain) {
    return { outcome: app ? `Deployed at ${hostOf(app)}.` : "Deployed." };
  }
  const address = hostOf({ domain: action.domain, name: action.name ?? "" });
  if (!app) {
    return {
      outcome: `Deployed. The user chooses who can open it on its Deployment page, under Address: ${address}.`,
    };
  }
  await publishApp(project, action.name ?? "", app.access, undefined, {
    domain: action.domain,
    aliases: action.aliases ?? [],
  });
  return { outcome: `Deployed at ${deployAddress(action)}.` };
}

async function appsPage(action: AssistantAction): Promise<Answer> {
  const access = action.how === "off" ? null : ((action.how as Access) ?? "private");
  const page = action.domain ? `The apps page at ${action.domain}` : "The apps page";
  try {
    await setAppsPage(access, undefined, undefined, action.domain || undefined);
  } catch (e) {
    if (access === "private") {
      return {
        outcome:
          "It needs a password first: the user chooses one in Settings → Server → Apps page.",
        failed: true,
      };
    }
    throw e;
  }
  return { outcome: access ? `${page} is on, ${ACCESS_WORDS[access]}.` : `${page} is off.` };
}

async function listIt(project: string | null, action: AssistantAction): Promise<Answer> {
  if (!project) return { outcome: "That needs a project's chat.", failed: true };
  await setListed(project, action.how !== "hide");
  return {
    outcome: action.how === "hide" ? "Hidden on the apps page." : "Listed on the apps page.",
  };
}

/** Cards applied by themselves in this window, so a re-render or a second
 * look never applies one twice. */
const appliedHere = new Set<string>();

/** Whether this app took the card up by itself, as one the user asked for. */
export const applyingHere = (id: string) => appliedHere.has(id);

/** Whether a card the user asked for applies without asking again: never
 * one that removes, stops, hides, turns Homelab, or moves an app. */
export function appliesAtOnce(action: AssistantAction, app?: PublishedApp): boolean {
  if (!action.asked || !SERVER_CARDS.has(action.type) || action.type === "set_homelab")
    return false;
  if (action.type === "set_apps_page") return action.how !== "off";
  if (action.type === "list_on_apps_page") return action.how !== "hide";
  if (action.type === "deploy_app") {
    const { from, dropped } = deployChange(action, app);
    return from === null && dropped.length === 0;
  }
  return true;
}

/**
 * A server card while it waits for the user: its question, Homelab's
 * warning when it switches Homelab, and its button, which applies it
 * through the host before the approval is recorded.
 */
export function ServerCard({
  scope,
  action,
  held,
  onChanged,
}: {
  scope: ChatTarget;
  action: AssistantAction;
  held?: string;
  onChanged: (conversation: Conversation) => void;
}) {
  const [overview, setOverview] = useState<PublishingOverview | null>(null);
  const [busy, setBusy] = useState<null | "apply" | "dismiss">(null);
  const [error, setError] = useState<string | null>(null);
  // Turning Homelab off would cut this computer off: the way back it tried.
  const [stranded, setStranded] = useState<{ tried: string | null } | null>(null);
  // Turned off anyway: this computer can no longer reach the server.
  const [lost, setLost] = useState(false);
  const server = connectedServer() !== null;
  useEffect(() => {
    if (!server) return;
    let live = true;
    void publishing()
      .then((found) => live && setOverview(found))
      .catch(() => undefined);
    return () => {
      live = false;
    };
  }, [server]);

  // A chat outside any project has no app of its own.
  const scopeName = typeof scope === "string" ? scope : scope.scope;
  const project = scopeName === TOP_LEVEL ? null : scopeName;
  const app = overview?.apps.find((found) => found.project_id === project);
  const [question, label] = serverCardQuestion(action, overview?.authentik.installed ?? false, app);
  // What the server reports after the change goes with it, so Mayka's next
  // turn sees it.
  const record = async (answer: Answer) =>
    approveAssistantAction(scope, action.id, {
      outcome: answer.outcome,
      failed: answer.failed,
      ...(answer.failed ? {} : { server_facts: await serverFacts(project ?? undefined) }),
    }).then(onChanged);

  const apply = async (anyway = false) => {
    setBusy("apply");
    setError(null);
    try {
      let answer: Answer;
      let reload = false;
      switch (action.type) {
        case "add_domain":
          answer = await addTheDomain(action).catch(refused);
          break;
        case "install_authentik":
          answer = await setUpAuthentik(action).catch(refused);
          break;
        case "set_app_access":
          answer = await changeAccess(action, project).catch(refused);
          break;
        case "deploy_app":
          answer = await deployIt(project, action).catch(refused);
          break;
        case "set_apps_page":
          answer = await appsPage(action).catch(refused);
          break;
        case "list_on_apps_page":
          answer = await listIt(project, action).catch(refused);
          break;
        default:
          if (action.how === "on") {
            answer = await setReach("private").then(() => {
              connectWhenHomelabReady();
              return {
                outcome:
                  "Homelab is on. The user signs in to Tailscale in Settings → Server → Homelab.",
              };
            }, refused);
            break;
          }
          if (overview && onTailscaleNow(overview)) {
            if (anyway) {
              // The answer would come back through Tailscale, which this
              // stops: the approval is recorded first, while it still can be.
              await record({ outcome: "Turning Homelab off; this computer loses the server." });
              await Promise.race([
                setReach("internet").catch(() => undefined),
                new Promise((done) => setTimeout(done, LOSING_ANSWER_MS)),
              ]);
              setLost(true);
              return;
            }
            const stuck = await moveBackFirst();
            if (stuck) {
              setStranded(stuck);
              return;
            }
            // This window now reaches the server elsewhere: it shows it from
            // there, as on a switch.
            reload = true;
          }
          answer = await setReach("internet").then(
            () => ({ outcome: "Homelab is off: apps are at the server's domains again." }),
            refused,
          );
      }
      await record(answer);
      if (answer.failed) setError(answer.outcome);
      if (reload) window.location.reload();
    } catch (e) {
      setError(String(e));
    } finally {
      setBusy(null);
    }
  };
  // The user asked for exactly this: it applies by itself, once, unless it
  // removes, stops, hides, turns Homelab, or moves an app (appliesAtOnce).
  const started = useRef(false);
  useEffect(() => {
    if (started.current || held || !server || !action.asked || appliedHere.has(action.id)) return;
    // Whether a deploy moves the app is known once the server answered.
    if (action.type === "deploy_app" && action.domain && !overview) return;
    if (!appliesAtOnce(action, app)) return;
    started.current = true;
    appliedHere.add(action.id);
    void apply();
    // apply reads this render's state; the card applies once, on the render
    // that first knows enough.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [overview, held, server]);

  const dismiss = () => {
    setBusy("dismiss");
    void dismissAssistantAction(scope, action.id)
      .then(onChanged)
      .catch((e) => setError(String(e)))
      .finally(() => setBusy(null));
  };

  if (lost) {
    return (
      <div className="assistant-action assistant-action--proposed" role="alert">
        <p className="assistant-action__desc">
          <strong>Homelab is off, and this computer can no longer reach the server.</strong> To
          connect again, on the same network as the server, run{" "}
          <code>sudo commitarium-host pair</code> on it and paste the line it prints into
          Commitarium, under Connect a server.
        </p>
      </div>
    );
  }
  const homelab = action.type === "set_homelab" && overview !== null;
  return (
    <div
      className={`assistant-action assistant-action--proposed ${held ? "assistant-action--held" : ""}`}
      title={`Why: ${action.reason}`}
    >
      <p className="assistant-action__desc">{homelab ? null : question}</p>
      {homelab && !stranded && (
        <p className="assistant-action__desc">
          <HomelabWarning
            on={action.how === "off"}
            apps={overview.apps.length}
            onTailscale={onTailscaleNow(overview)}
          />
        </p>
      )}
      {stranded && (
        <p className="assistant-action__desc">
          <StrandedWarning tried={stranded.tried} />
        </p>
      )}
      {action.type === "add_domain" && (
        <p className="muted assistant-action__desc">
          Its DNS record goes in at your DNS provider first; Commitarium checks it now.
        </p>
      )}
      {!server && (
        <p className="muted assistant-action__desc">
          This changes your server: connect one in Settings → Server first.
        </p>
      )}
      {error && <div className="banner banner--error">{error}</div>}
      {held && <p className="muted assistant-action__held">{held}</p>}
      <div className="row" hidden={!!held}>
        <button
          className="primary"
          onClick={() => void apply(stranded !== null)}
          disabled={busy !== null || !server || (action.type === "set_homelab" && !overview)}
        >
          {busy === "apply"
            ? action.type === "install_authentik"
              ? "Setting up, a few minutes…"
              : "Applying…"
            : stranded
              ? "Turn off anyway"
              : label}
        </button>
        <button className="ghost" onClick={dismiss} disabled={busy !== null}>
          Not now
        </button>
      </div>
    </div>
  );
}
