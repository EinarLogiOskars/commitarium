import { useCallback, useEffect, useState } from "react";
import { getWorkspace, listFeatures } from "../api/features";
import { openAssistantOn, startChatOn } from "../attention/askAssistant";
import {
  deploy,
  deployments as readDeployments,
  revealAppPassword,
  setAppAccess,
  setAppGroups,
  setAppPassword,
  setAppsPage,
  setListed,
  checkAppsPageName,
  openExternal,
  publishApp,
  publishing as readPublishing,
  stopDeployment,
  unpublishApp,
  type Access,
  type AppAddress,
  type AppsPage,
  type Deploy,
  type Deployment,
  type DomainShown,
  type PublishedApp,
} from "../ipc";

const POLL_MS = 5000;
const SHOWN_HISTORY = 5;

/** A work order merged into main, as a version that can be deployed. */
interface Version {
  commitId: string;
  title: string;
}

/** A project's deployment, its address, and the versions it can deploy. */
function useDeployment(projectId: string) {
  const [deployment, setDeployment] = useState<Deployment | null>(null);
  const [published, setPublished] = useState<PublishedApp | null>(null);
  // Where addresses go: the domain, or on the private network the
  // Tailscale name once it is ready (HOMELAB_PLAN.md).
  const [domain, setDomain] = useState<string | null>(null);
  // The server's domains, each with its sign-in and apps page.
  const [domains, setDomains] = useState<DomainShown[]>([]);
  const [privateNet, setPrivateNet] = useState(false);
  // On the private network: its apps page, the one there is.
  const [tailnetAppsPage, setTailnetAppsPage] = useState<AppsPage | null>(null);
  const [versions, setVersions] = useState<Version[]>([]);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  // A failed read, as while the server restarts for an update: shown until
  // a later read succeeds, unlike an action's error, which stays until the
  // next action.
  const [readError, setReadError] = useState<string | null>(null);

  const load = useCallback(async () => {
    try {
      const [all, overview] = await Promise.all([readDeployments(), readPublishing()]);
      setDeployment(all.find((d) => d.project_id === projectId) ?? null);
      setPublished(overview.apps.find((app) => app.project_id === projectId) ?? null);
      const onTailnet = overview.reach === "private";
      setPrivateNet(onTailnet);
      setDomain(
        onTailnet
          ? overview.tailscale?.state === "ready"
            ? overview.tailscale.name
            : null
          : (overview.domains[0]?.name ?? null),
      );
      setDomains(onTailnet ? [] : overview.domains);
      setTailnetAppsPage(onTailnet ? overview.apps_page : null);
      setReadError(null);
    } catch (e) {
      setReadError(String(e));
    }
  }, [projectId]);

  useEffect(() => {
    void load();
    const timer = setInterval(() => void load(), POLL_MS);
    return () => clearInterval(timer);
  }, [load]);

  // The merged work orders, newest first, each the version it left main at.
  useEffect(() => {
    let live = true;
    void listFeatures(projectId)
      .then((features) =>
        Promise.all(
          features
            .filter((f) => f.state === "completed")
            .map((f) =>
              getWorkspace(projectId, f.id)
                .then((w) => ({
                  commitId: w.merge?.merge_commit_id ?? "",
                  title: f.title,
                  at: w.merge?.merged_at ?? "",
                }))
                .catch(() => null),
            ),
        ),
      )
      .then((found) => {
        if (!live) return;
        const merged = found.filter((v): v is Version & { at: string } => !!v?.commitId);
        merged.sort((a, b) => b.at.localeCompare(a.at));
        setVersions(merged);
      })
      .catch(() => {});
    return () => {
      live = false;
    };
  }, [projectId]);

  const act = async (work: () => Promise<unknown>) => {
    setBusy(true);
    setError(null);
    try {
      await work();
      await load();
    } catch (e) {
      setError(String(e));
    } finally {
      setBusy(false);
    }
  };

  const label = (commitId: string) =>
    versions.find((v) => v.commitId === commitId)?.title ?? commitId.slice(0, 8);
  const current = deployment?.current ?? null;
  const state = deployment?.status?.state ?? (current ? "starting" : "stopped");
  const failure = state === "failed" ? deployment?.status?.error : null;
  // Merged work since the deployed version: what Deploy latest would add.
  const newer = current
    ? versions.findIndex((v) => v.commitId === current.commit_id)
    : versions.length;
  const behind = newer < 0 ? null : newer;
  return {
    deployment,
    published,
    domain,
    domains,
    privateNet,
    tailnetAppsPage,
    versions,
    busy,
    error: error ?? readError,
    act,
    label,
    current,
    state,
    failure,
    behind,
  };
}

/** What went wrong, with Mayka's help when it is a DNS record to add. */
function DeployError({ error }: { error: string }) {
  const record = error.includes("does not point at this server yet");
  return (
    <p className="sync-card__error">
      {error}
      {record && (
        <>
          {" "}
          <button
            className="shell-card__link"
            onClick={() =>
              startChatOn(
                { kind: "serving" },
                `Help me add this DNS record at my DNS provider. Commitarium says: ${error}`,
              )
            }
          >
            Ask Mayka to help
          </button>
        </>
      )}
    </p>
  );
}

function DeployState({ shown }: { shown: ReturnType<typeof useDeployment> }) {
  const { current, state } = shown;
  return (
    <span className="shell-card__meta shell-preview__state">
      <span className={`dot dot--${STATE_TONE[current ? state : "none"]}`} aria-hidden />
      {current ? stateLabel(state) : "Not deployed"}
    </span>
  );
}

function Behind({ behind }: { behind: number | null }) {
  if (behind === null) return null;
  return (
    <span className="muted shell-card__meta">
      {behind === 0
        ? "Up to date with your work"
        : `${behind} newer change${behind === 1 ? "" : "s"} not deployed`}
    </span>
  );
}

// The project's deployment on a server (PUBLISHING_PLAN.md) at a glance: its
// state and address, with Deploy latest. The rest is on its page.
export function DeployCard({
  projectId,
  onOpenPage,
}: {
  projectId: string;
  onOpenPage: () => void;
}) {
  const shown = useDeployment(projectId);
  const { current, published, busy, error, failure, act, behind } = shown;
  return (
    <section className="shell-card">
      <div className="shell-card__top">
        <h2 className="shell-card__head">Deployment</h2>
        <DeployState shown={shown} />
      </div>
      {current && published ? (
        <p className="shell-card__text">
          <button className="shell-card__link" onClick={() => void openExternal(published.url)}>
            {published.url.replace("https://", "")} ↗
          </button>
        </p>
      ) : (
        !current && (
          <p className="muted shell-card__text">Put a version on your server for others to use.</p>
        )
      )}
      {failure && <p className="sync-card__error">It did not start.</p>}
      {current && published?.record_problem && published.record_problem !== error && (
        <DeployError error={published.record_problem} />
      )}
      {error && <DeployError error={error} />}
      <div className="row deploy-actions">
        <button
          className="primary"
          disabled={busy}
          onClick={() => void act(() => deploy(projectId))}
        >
          {busy ? "Deploying…" : current ? "Deploy latest" : "Deploy"}
        </button>
        {current && <Behind behind={behind} />}
      </div>
      <div className="row deploy-links">
        <button className="shell-card__link" onClick={onOpenPage}>
          Deployment page
        </button>
      </div>
    </section>
  );
}

// Everything about the deployment: version, address and access, earlier
// versions and roll back, and stopping it.
export function DeploymentPage({ projectId, name }: { projectId: string; name: string }) {
  const shown = useDeployment(projectId);
  const {
    deployment,
    published,
    domain,
    domains,
    privateNet,
    tailnetAppsPage,
    versions,
    busy,
    error,
    act,
    label,
    current,
    failure,
    behind,
  } = shown;
  return (
    <section className="deploy-page">
      <div className="shell-card__top">
        <h2>Deployment</h2>
        <DeployState shown={shown} />
      </div>
      <p className="muted">
        A deployment is the version of this app that visitors see. Keep working and trying things in
        the preview; when it&apos;s ready, deploy it. Its data is its own, so nothing you do in the
        preview touches what visitors saved.
      </p>
      {current && (
        <dl className="deploy-facts">
          <dt>Version</dt>
          <dd>
            After “{label(current.commit_id)}”
            <span className="muted"> · {ago(current.deployed_at)}</span>
          </dd>
          {published && (
            <>
              <dt>{published.urls.length > 1 ? "Addresses" : "Address"}</dt>
              <dd>
                {(published.urls.length > 0 ? published.urls : [published.url]).map(
                  (url, index) => (
                    <span key={url}>
                      {index > 0 && ", "}
                      <button className="shell-card__link" onClick={() => void openExternal(url)}>
                        {url.replace("https://", "")} ↗
                      </button>
                    </span>
                  ),
                )}
                <span className="muted">
                  {" "}
                  ·{" "}
                  {published.access === "private"
                    ? "needs the password"
                    : published.access === "company"
                      ? "company sign-in"
                      : "open to anyone"}
                </span>
              </dd>
            </>
          )}
        </dl>
      )}
      {failure && <p className="sync-card__error">It did not start: {failure}</p>}
      {current && published?.record_problem && published.record_problem !== error && (
        <DeployError error={published.record_problem} />
      )}
      {error && <DeployError error={error} />}
      <div className="row deploy-actions">
        <button
          className="primary"
          disabled={busy}
          onClick={() => void act(() => deploy(projectId))}
        >
          {busy ? "Deploying…" : current ? "Deploy latest" : "Deploy"}
        </button>
        {current && <Behind behind={behind} />}
      </div>
      {versions.length > 1 && (
        <select
          className="deploy-version"
          aria-label="Deploy an earlier version"
          value=""
          disabled={busy}
          onChange={(e) => {
            const commit = e.target.value;
            if (commit) void act(() => deploy(projectId, commit));
          }}
        >
          <option value="">Deploy an earlier version…</option>
          {versions.map((v) => (
            <option key={v.commitId} value={v.commitId}>
              After “{v.title}”
            </option>
          ))}
        </select>
      )}
      {current && (
        <Address
          projectId={projectId}
          name={name}
          domain={domain}
          domains={domains}
          privateNet={privateNet}
          tailnetAppsPage={tailnetAppsPage}
          published={published}
          busy={busy}
          act={act}
          onStop={() => void act(() => stopDeployment(projectId))}
        />
      )}
      <History
        history={deployment?.history ?? []}
        current={current}
        label={label}
        busy={busy}
        onRollBack={(commit) => void act(() => deploy(projectId, commit))}
      />
    </section>
  );
}

const STATE_TONE: Record<string, string> = {
  running: "ok",
  starting: "warn",
  failed: "bad",
  stopped: "muted",
  none: "muted",
};

/** Where an app is published: its main address, www for a bare domain, and
 * its extra addresses (MULTI_DOMAIN_PLAN.md, M2). */
export interface Place {
  domain: string;
  name: string;
  www: boolean;
  aliases: AppAddress[];
}

/** The host an address answers at: the empty name is the domain itself. */
export const hostOf = ({ domain, name }: AppAddress) => (name ? `${name}.${domain}` : domain);

/** What changed between where an app is and where it would be, for its
 * Change it button; nothing when they are the same. */
export function placeChanged(published: PublishedApp, place: Place): boolean {
  const aliases = (list: AppAddress[]) => list.map(hostOf).join(" ");
  return (
    place.domain !== published.domain ||
    place.name.trim().toLowerCase() !== published.name ||
    (place.name.trim() === "" && place.www !== published.www) ||
    aliases(place.aliases) !== aliases(published.aliases)
  );
}

// The address fields: a domain picker when the server has more than one,
// the name (empty for the domain itself, on the internet), www for a bare
// name, and extra addresses. On the private network only the name.
function AddressFields({
  place,
  onChange,
  domains,
  privateNet,
  suffix,
  label,
}: {
  place: Place;
  onChange: (place: Place) => void;
  domains: string[];
  privateNet: boolean;
  /** Shown after the name on the private network, where there is no domain. */
  suffix: React.ReactNode;
  label: string;
}) {
  const several = domains.length > 1;
  const bare = !privateNet && place.name.trim() === "";
  const pick = (value: string, change: (domain: string) => void) => (
    <select aria-label="Domain" value={value} onChange={(e) => change(e.target.value)}>
      {domains.map((domain) => (
        <option key={domain} value={domain}>
          {domain}
        </option>
      ))}
    </select>
  );
  return (
    <>
      <label className="muted">
        {label}
        <span className="deploy-address__name">
          <input
            aria-label={privateNet ? "Name" : "Address name"}
            placeholder={privateNet ? undefined : "empty for the domain itself"}
            value={place.name}
            autoCapitalize="off"
            autoCorrect="off"
            spellCheck={false}
            onChange={(e) => onChange({ ...place, name: e.target.value })}
          />
          {privateNet ? (
            suffix
          ) : several ? (
            <>
              <span className="muted">.</span>
              {pick(place.domain, (domain) => onChange({ ...place, domain }))}
            </>
          ) : (
            <span className="muted">.{place.domain}</span>
          )}
        </span>
      </label>
      {bare && (
        <>
          <p className="muted shell-card__text">
            With no name, it is {place.domain} itself: the domain&apos;s own website.
          </p>
          <label className="set__row">
            <input
              type="checkbox"
              checked={place.www}
              onChange={(e) => onChange({ ...place, www: e.target.checked })}
            />
            <span>www.{place.domain} sends people here too</span>
          </label>
        </>
      )}
      {!privateNet && (
        <div className="deploy-aliases">
          {place.aliases.length > 0 && (
            <p className="muted shell-card__text">
              Extra addresses: the app is reached at all of them, and can pick its language from the
              address it was opened on.
            </p>
          )}
          {place.aliases.map((alias, index) => {
            const change = (next: AppAddress) =>
              onChange({
                ...place,
                aliases: place.aliases.map((old, at) => (at === index ? next : old)),
              });
            return (
              <span className="deploy-address__name" key={index}>
                <input
                  aria-label="Extra address name"
                  placeholder="empty for the domain itself"
                  value={alias.name}
                  autoCapitalize="off"
                  autoCorrect="off"
                  spellCheck={false}
                  onChange={(e) => change({ ...alias, name: e.target.value })}
                />
                {several ? (
                  <>
                    <span className="muted">.</span>
                    {pick(alias.domain, (domain) => change({ ...alias, domain }))}
                  </>
                ) : (
                  <span className="muted">.{alias.domain}</span>
                )}
                <button
                  className="shell-card__link"
                  onClick={() =>
                    onChange({ ...place, aliases: place.aliases.filter((_, at) => at !== index) })
                  }
                >
                  Remove
                </button>
              </span>
            );
          })}
          <button
            className="shell-card__link"
            onClick={() =>
              onChange({
                ...place,
                // Another domain first: the usual extra address is the same
                // website at a second domain.
                aliases: [
                  ...place.aliases,
                  { domain: domains.find((d) => d !== place.domain) ?? place.domain, name: "" },
                ],
              })
            }
          >
            Add an extra address
          </button>
        </div>
      )}
    </>
  );
}

/** The address fields' answer, as the host takes it: names trimmed, and
 * www only for a bare name. */
function placing(place: Place) {
  return {
    domain: place.domain,
    www: place.name.trim() === "" ? place.www : undefined,
    aliases: place.aliases.map((alias) => ({
      domain: alias.domain,
      name: alias.name.trim().toLowerCase(),
    })),
  };
}

function Address({
  projectId,
  name,
  domain,
  domains,
  privateNet,
  tailnetAppsPage,
  published,
  busy,
  act,
  onStop,
}: {
  projectId: string;
  name: string;
  /** Where addresses go: the first domain, or the Tailscale name. */
  domain: string | null;
  /** The server's domains, with each one's sign-in and apps page. */
  domains: DomainShown[];
  /** Only the owner's devices reach the apps, on their Tailscale network:
   * apps get ports of the Tailscale name, not names under a domain. */
  privateNet: boolean;
  tailnetAppsPage: AppsPage | null;
  published: PublishedApp | null;
  busy: boolean;
  act: (work: () => Promise<unknown>) => Promise<void>;
  onStop: () => void;
}) {
  const fresh = (): Place =>
    published
      ? {
          domain: published.domain || (domain ?? ""),
          name: published.name,
          www: published.www,
          aliases: published.aliases.map((alias) => ({ domain: alias.domain, name: alias.name })),
        }
      : { domain: domain ?? "", name: slug(name), www: true, aliases: [] };
  const [place, setPlace] = useState<Place>(fresh);
  const [access, setAccess] = useState<Access>(published?.access ?? "private");
  const [password, setPassword] = useState("");
  const [changing, setChanging] = useState(false);
  const [renaming, setRenaming] = useState(false);
  const [changed, setChanged] = useState(false);
  const [shownPassword, setShownPassword] = useState<string | null | undefined>(undefined);
  const [editingGroups, setEditingGroups] = useState(false);
  const [groups, setGroups] = useState("");
  const empty = password === "";
  const names = domains.map((item) => item.name);
  // A cookie cannot cross domains: company sign-in is per domain, so it is
  // the one at the app's main address that counts.
  const at = (name: string) => domains.find((item) => item.name === name);
  const companySignIn = (name: string) => !privateNet && at(name)?.sign_in != null;
  const appsPage = privateNet ? tailnetAppsPage : (at(published?.domain ?? "")?.apps_page ?? null);
  // Company sign-in chosen before a provider is connected: say where to.
  const [needsSignIn, setNeedsSignIn] = useState(false);
  // Company sign-in needs a domain on the internet: not on a private network.
  const companyOption = (name: string) =>
    privateNet ? null : (
      <option value="company">
        {companySignIn(name) ? "Company sign-in" : "Company sign-in (set up first)"}
      </option>
    );
  const setUpSignIn = (name: string) =>
    needsSignIn &&
    !companySignIn(name) && (
      <>
        <p className="muted shell-card__text">
          Company sign-in lets people open it with their work account, such as Microsoft or Google.
          It is connected once for each domain, in Settings → Server.
        </p>
        <div className="row deploy-links">
          <button
            className="shell-card__link"
            onClick={() =>
              openAssistantOn(
                { kind: "sign_in" },
                `Help me set up company sign-in for this app, at ${name}.`,
              )
            }
          >
            Set it up with Mayka
          </button>
        </div>
      </>
    );

  const stop = (
    <button className="shell-card__link" disabled={busy} onClick={onStop}>
      Stop deployment
    </button>
  );
  // On the private network an app's address is a port of the server's
  // Tailscale name; its name is only how it is called and signed in to.
  const suffix = privateNet ? null : <span className="muted">.{domain}</span>;
  if (!domain) {
    return (
      <>
        <p className="muted shell-card__text">
          {privateNet
            ? "It gets an address once Tailscale is set up, in Settings → Server."
            : "It has no address yet. In Settings → Server, add your domain, or turn on Homelab mode for a server at home."}
        </p>
        <div className="row deploy-links">{stop}</div>
      </>
    );
  }
  // A name is needed on the private network; on the internet, empty is the
  // domain itself.
  const named = privateNet ? place.name.trim() !== "" : true;
  if (published) {
    const signIn = companySignIn(published.domain);
    // A password set while the app is open to anyone also makes it private.
    const save = () =>
      void act(async () => {
        await setAppPassword(projectId, password);
        if (published.access === "public") await setAppAccess(projectId, "private");
        setChanging(false);
        setChanged(true);
        setPassword("");
      });
    const choose = (next: Access) => {
      setNeedsSignIn(next === "company" && !signIn);
      if (next === "company" && !signIn) return;
      void act(() =>
        setAppAccess(projectId, next).catch((e) => {
          // Never had a password: ask for one, then make it private.
          if (next === "private") setChanging(true);
          else throw e;
        }),
      );
    };
    const saveGroups = () =>
      void act(async () => {
        await setAppGroups(
          projectId,
          groups
            .split(",")
            .map((group) => group.trim())
            .filter(Boolean),
        );
        setEditingGroups(false);
      });
    const moves = placeChanged(published, place);
    return (
      <>
        <label className="muted deploy-access">
          Who can open it
          <select
            value={published.access}
            disabled={busy}
            onChange={(e) => choose(e.target.value as Access)}
          >
            <option value="public">Anyone, no password</option>
            <option value="private">Only with a password</option>
            {companyOption(published.domain)}
          </select>
        </label>
        {setUpSignIn(published.domain)}
        {published.access === "company" && (
          <p className="muted shell-card__text">
            People sign in with their company account.{" "}
            {published.groups.length === 0
              ? "Anyone at the company can open it."
              : `Only ${published.groups.join(", ")} can open it.`}
          </p>
        )}
        {published.access === "company" && editingGroups && (
          <div className="row deploy-actions">
            <input
              aria-label="Groups"
              placeholder="Groups, such as sales, finance"
              autoFocus
              value={groups}
              onChange={(e) => setGroups(e.target.value)}
              onKeyDown={(e) => e.key === "Enter" && saveGroups()}
            />
            <button disabled={busy} onClick={saveGroups}>
              Save
            </button>
            <button className="ghost" onClick={() => setEditingGroups(false)}>
              Cancel
            </button>
          </div>
        )}
        {appsPage ? (
          <label className="set__row deploy-listed">
            <input
              type="checkbox"
              checked={published.listed}
              disabled={busy}
              onChange={(e) => void act(() => setListed(projectId, e.target.checked))}
            />
            <span>
              Listed on the{" "}
              <button className="shell-card__link" onClick={() => void openExternal(appsPage.url)}>
                apps page
              </button>
            </span>
          </label>
        ) : (
          <AppsPageOffer
            domain={privateNet ? domain : published.domain || domain}
            privateNet={privateNet}
            access={published.access}
            busy={busy}
            act={act}
          />
        )}
        {published.access === "private" && (
          <p className="muted shell-card__text">
            People sign in with the username <strong>{published.name}</strong> and the password you
            chose.{changed && " The new password is in use."}
          </p>
        )}
        {published.access === "private" && shownPassword !== undefined && (
          <p className="shell-card__text">
            {shownPassword === null ? (
              <span className="muted">
                This password was set before Commitarium kept them. Change it once to see it here.
              </span>
            ) : (
              <>
                Password: <code className="boot__command">{shownPassword}</code>
              </>
            )}
          </p>
        )}
        {renaming && (
          <div className="deploy-address">
            <AddressFields
              place={place}
              onChange={setPlace}
              domains={names}
              privateNet={privateNet}
              suffix={suffix}
              label="New address"
            />
            {moves && (
              <p className="sync-card__error">
                {published.url.replace("https://", "")} stops working once you change it, unless it
                stays one of its addresses. Anyone with an old link needs the new one.
              </p>
            )}
            <div className="row deploy-actions">
              <button
                disabled={busy || !named || !moves}
                onClick={() =>
                  void act(async () => {
                    await publishApp(
                      projectId,
                      place.name,
                      published.access,
                      undefined,
                      privateNet ? {} : placing(place),
                    );
                    setRenaming(false);
                  })
                }
              >
                Change it
              </button>
              <button className="ghost" onClick={() => setRenaming(false)}>
                Cancel
              </button>
            </div>
          </div>
        )}
        {changing && (
          <div className="row deploy-actions">
            <input
              aria-label="New password"
              autoCapitalize="off"
              autoCorrect="off"
              spellCheck={false}
              placeholder="New password"
              autoFocus
              value={password}
              onChange={(e) => setPassword(e.target.value)}
              onKeyDown={(e) => e.key === "Enter" && !empty && save()}
            />
            <button disabled={busy || empty} onClick={save}>
              Save
            </button>
            <button className="ghost" onClick={() => setChanging(false)}>
              Cancel
            </button>
          </div>
        )}
        <div className="row deploy-links">
          {published.access === "company" && !editingGroups && (
            <button
              className="shell-card__link"
              disabled={busy}
              onClick={() => {
                setGroups(published.groups.join(", "));
                setEditingGroups(true);
              }}
            >
              Choose groups
            </button>
          )}
          {published.access === "private" && !changing && (
            <button
              className="shell-card__link"
              disabled={busy}
              onClick={() => {
                setChanged(false);
                setShownPassword(undefined);
                setChanging(true);
              }}
            >
              Change password
            </button>
          )}
          {published.access === "private" && !changing && (
            <button
              className="shell-card__link"
              disabled={busy}
              onClick={() =>
                shownPassword === undefined
                  ? void revealAppPassword(projectId).then(setShownPassword)
                  : setShownPassword(undefined)
              }
            >
              {shownPassword === undefined ? "Show password" : "Hide password"}
            </button>
          )}
          {!renaming && (
            <button
              className="shell-card__link"
              disabled={busy}
              onClick={() => {
                setPlace(fresh());
                setRenaming(true);
              }}
            >
              Change address
            </button>
          )}
          <button
            className="shell-card__link"
            disabled={busy}
            onClick={() => void act(() => unpublishApp(projectId))}
            title="Removes the address; the deployment keeps running"
          >
            Remove address
          </button>
          {stop}
        </div>
      </>
    );
  }
  const signIn = companySignIn(place.domain);
  return (
    <div className="deploy-address">
      <p className="muted shell-card__text">
        {privateNet
          ? "Put it online on your private network: it gets its own address there, for your devices with Tailscale."
          : "Give it an address so people can open it:"}
      </p>
      <AddressFields
        place={place}
        onChange={setPlace}
        domains={names}
        privateNet={privateNet}
        suffix={suffix}
        label={privateNet ? "Name" : "Address"}
      />
      <label className="muted deploy-access">
        Who can open it
        <select
          value={access}
          onChange={(e) => {
            const next = e.target.value as Access;
            setNeedsSignIn(next === "company" && !signIn);
            setAccess(next);
          }}
        >
          <option value="private">Only with a password</option>
          <option value="public">Anyone, no password</option>
          {companyOption(place.domain)}
        </select>
      </label>
      {setUpSignIn(place.domain)}
      {access === "private" && (
        <label className="muted">
          Password, to share with the people you want
          <input
            placeholder="A password to share"
            autoCapitalize="off"
            autoCorrect="off"
            spellCheck={false}
            value={password}
            onChange={(e) => setPassword(e.target.value)}
          />
          <span>
            {place.name.trim()
              ? `They sign in with the username ${place.name.trim().toLowerCase()}.`
              : "They sign in with an empty username."}
          </span>
        </label>
      )}
      <button
        disabled={
          busy || !named || (access === "private" && empty) || (access === "company" && !signIn)
        }
        onClick={() =>
          void act(() =>
            publishApp(
              projectId,
              place.name,
              access,
              access === "private" ? password : undefined,
              privateNet ? {} : placing(place),
            ),
          )
        }
      >
        Put online
      </button>
      <div className="row deploy-links">{stop}</div>
    </div>
  );
}

/** Whether this device was asked about the apps page for this domain. */
const offerKey = (domain: string) => `commitarium:apps-page-offer:${domain}`;

function asked(domain: string): boolean {
  try {
    return localStorage.getItem(offerKey(domain)) !== null;
  } catch {
    return false;
  }
}

// Once an app has an address on a server without an apps page, offer one,
// with this app's access; asked once (APPS_PAGE_PLAN.md, A3).
function AppsPageOffer({
  domain,
  privateNet,
  access,
  busy,
  act,
}: {
  domain: string;
  privateNet: boolean;
  access: Access;
  busy: boolean;
  act: (work: () => Promise<unknown>) => Promise<void>;
}) {
  const [hidden, setHidden] = useState(() => asked(domain));
  const [password, setPassword] = useState("");
  const [name, setName] = useState("apps");
  // Whether apps.<domain> is free: a company may use it already. Asked once,
  // so the offer says so before Turn on rather than after.
  const [taken, setTaken] = useState<string | null>(null);
  useEffect(() => {
    if (hidden) return;
    let live = true;
    checkAppsPageName("apps", privateNet ? undefined : domain).catch(
      (e) => live && setTaken(String(e)),
    );
    return () => {
      live = false;
    };
  }, [hidden, domain, privateNet]);
  if (hidden) return null;
  const dismiss = () => {
    try {
      localStorage.setItem(offerKey(domain), "asked");
    } catch {
      // Not kept: it is asked again next time, nothing worse.
    }
    setHidden(true);
  };
  const how =
    access === "company"
      ? "behind company sign-in, where each person sees the apps they can open"
      : access === "private"
        ? "behind a password of its own"
        : "open to anyone";
  return (
    <div className="deploy-offer">
      <p className="shell-card__text">
        <strong>Turn on an apps page?</strong> One address lists your apps in one place, to open in
        any browser or on a phone. It would be {how}, like this app. Either way, you can turn it on,
        change it or turn it off any time in Settings → Server → Apps page.
      </p>
      {taken && <p className="muted shell-card__text">{taken}</p>}
      <label className="muted" hidden={privateNet}>
        Address
        <span className="deploy-address__name">
          <input
            aria-label="Apps page address"
            value={name}
            autoCapitalize="off"
            autoCorrect="off"
            spellCheck={false}
            onChange={(e) => {
              setName(e.target.value);
              setTaken(null);
            }}
          />
          <span className="muted">.{domain}</span>
        </span>
      </label>
      {access === "private" && (
        <input
          aria-label="Apps page password"
          placeholder="A password for the apps page"
          autoCapitalize="off"
          autoCorrect="off"
          spellCheck={false}
          value={password}
          onChange={(e) => setPassword(e.target.value)}
        />
      )}
      <div className="row deploy-actions">
        <button
          className="primary"
          disabled={busy || !name.trim() || (access === "private" && !password)}
          onClick={() =>
            void act(async () => {
              await setAppsPage(
                access,
                access === "private" ? password : undefined,
                name,
                privateNet ? undefined : domain,
              );
              dismiss();
            })
          }
        >
          Turn on
        </button>
        <button className="ghost" disabled={busy} onClick={dismiss}>
          Not now
        </button>
      </div>
    </div>
  );
}

function History({
  history,
  current,
  label,
  busy,
  onRollBack,
}: {
  history: Deploy[];
  current: Deploy | null;
  label: (commitId: string) => string;
  busy: boolean;
  onRollBack: (commitId: string) => void;
}) {
  const earlier = history
    .filter((d) => d.commit_id !== current?.commit_id)
    .filter((d, i, all) => all.findIndex((o) => o.commit_id === d.commit_id) === i)
    .slice(0, SHOWN_HISTORY);
  if (earlier.length === 0) return null;
  return (
    <details className="deploy-history">
      <summary className="muted">Earlier deploys</summary>
      {earlier.map((d) => (
        <div className="row" key={`${d.commit_id}-${d.deployed_at}`}>
          <span className="muted">
            “{label(d.commit_id)}” · {ago(d.deployed_at)}
          </span>
          <button
            className="shell-card__link"
            disabled={busy}
            onClick={() => onRollBack(d.commit_id)}
          >
            Roll back
          </button>
        </div>
      ))}
    </details>
  );
}

function stateLabel(state: string): string {
  switch (state) {
    case "running":
      return "Live";
    case "starting":
      return "Starting…";
    case "failed":
      return "Failed";
    default:
      return "Stopped";
  }
}

/** A plain address name from the project's name: "Simple To-Do" → "simple-to-do". */
export function slug(name: string): string {
  return name
    .normalize("NFKD")
    .replace(/[\u0300-\u036f]/g, "")
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, "-")
    .replace(/^-+|-+$/g, "")
    .slice(0, 40);
}

function ago(seconds: number): string {
  const minutes = Math.floor((Date.now() / 1000 - seconds) / 60);
  if (minutes < 1) return "just now";
  if (minutes < 60) return `${minutes} min ago`;
  const hours = Math.floor(minutes / 60);
  if (hours < 24) return `${hours} h ago`;
  return `${Math.floor(hours / 24)} days ago`;
}
