import { useCallback, useEffect, useState } from "react";
import { ApiError } from "../api/client";
import {
  installMarketplaceApp,
  listMarketplaceApps,
  type MarketplaceApp,
} from "../api/marketplace";
import { openAppWindow } from "../ipc";
import { iconColor, initials } from "./AppsView";

// The marketplace (MARKETPLACE_PLAN.md): apps the people on it released.
// Installing one makes the user's own copy, which they can change with their
// assistant like any app they made.
export function MarketplaceView({ reachable }: { reachable: boolean }) {
  const [apps, setApps] = useState<MarketplaceApp[] | null>(null);
  const [notice, setNotice] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [installing, setInstalling] = useState<string | null>(null);

  const load = useCallback(() => {
    listMarketplaceApps()
      .then((list) => {
        setApps(list);
        setNotice(null);
      })
      .catch((e) => {
        setApps([]);
        if (e instanceof ApiError && e.code === "marketplace_not_signed_in")
          setNotice("Sign in to the marketplace in Settings to see the apps on it.");
        else if (e instanceof ApiError && e.status === 404)
          setNotice("This Commitarium has no marketplace set up.");
        else setError(describe(e));
      });
  }, []);
  useEffect(() => {
    if (reachable) load();
  }, [reachable, load]);

  const install = async (app: MarketplaceApp) => {
    const key = `${app.owner}/${app.name}`;
    setInstalling(key);
    setError(null);
    try {
      const installed = await installMarketplaceApp(app.owner, app.name);
      setApps(
        (current) =>
          current?.map((a) =>
            a === app
              ? {
                  ...a,
                  installed_project_id: installed.project_id,
                  installed_version: installed.version,
                }
              : a,
          ) ?? null,
      );
      void openAppWindow(installed.project_id, app.title).catch(() => {});
    } catch (e) {
      setError(describe(e));
    } finally {
      setInstalling(null);
    }
  };

  const open = (app: MarketplaceApp) => {
    if (!app.installed_project_id) return;
    openAppWindow(app.installed_project_id, app.title).catch((e) => setError(String(e)));
  };

  return (
    <section className="panel">
      <div className="panel__head">
        <h2>Marketplace</h2>
        <button className="ghost" onClick={load} disabled={installing !== null}>
          Refresh
        </button>
      </div>
      <p className="muted set__note">
        Apps the people on your marketplace released. Installing one gives you your own copy to use
        and change.
      </p>
      {error && <div className="banner banner--error">{error}</div>}
      {notice ? (
        <p className="muted">{notice}</p>
      ) : apps === null ? (
        <p className="muted">Loading…</p>
      ) : apps.length === 0 ? (
        <p className="muted">No apps released yet.</p>
      ) : (
        <div className="apps">
          {apps.map((app) => {
            const key = `${app.owner}/${app.name}`;
            return (
              <div key={key} className="apps__tile market__tile">
                <span
                  className="apps__icon"
                  style={{ background: iconColor(app.title) }}
                  aria-hidden
                >
                  {initials(app.title)}
                </span>
                <span className="apps__name">{app.title}</span>
                <span className="apps__state muted">
                  by {app.owner} · {app.version}
                  {app.stars > 0 && ` · ★ ${app.stars}`}
                </span>
                {app.yours ? (
                  <span className="muted market__mark">Yours</span>
                ) : app.installed_project_id ? (
                  <button onClick={() => open(app)}>
                    Open{app.installed_version ? ` (${app.installed_version})` : ""}
                  </button>
                ) : (
                  <button
                    className="primary"
                    disabled={installing !== null}
                    onClick={() => void install(app)}
                  >
                    {installing === key ? "Installing…" : "Install"}
                  </button>
                )}
              </div>
            );
          })}
        </div>
      )}
    </section>
  );
}

function describe(e: unknown): string {
  if (e instanceof ApiError) return e.message;
  return String(e);
}
