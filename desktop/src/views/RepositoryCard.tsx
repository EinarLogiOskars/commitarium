import { useEffect, useRef, useState } from "react";
import { getRepositoryOverview, type RepositoryOverview } from "../api/projects";
import { ApiError } from "../api/client";
import { openExternal } from "../ipc";
import { Markdown } from "./Markdown";

// Repository overview from the internal Forgejo repo (the authoritative version
// the agents work on): head commit, top-level tree, and the root README.
export function RepositoryCard({ projectId }: { projectId: string }) {
  const [overview, setOverview] = useState<RepositoryOverview | null>(null);
  const [error, setError] = useState<{ code?: string; message: string } | null>(null);
  const [expanded, setExpanded] = useState(false);
  // Only offer the expand toggle when the clamped README actually overflows —
  // a short README that fits shows in full with no toggle. A ResizeObserver
  // re-measures once the markdown has real height (and on window resize), which
  // a one-shot effect misses when it runs before layout settles.
  const [overflowing, setOverflowing] = useState(false);
  const bodyRef = useRef<HTMLDivElement | null>(null);

  useEffect(() => {
    const el = bodyRef.current;
    if (!el) return;
    const check = () => {
      // Don't remeasure while expanded (no clamp); the toggle is kept visible
      // via `|| expanded` so "Show less" stays until the user collapses.
      if (!expanded) setOverflowing(el.scrollHeight - el.clientHeight > 4);
    };
    check();
    const ro = new ResizeObserver(check);
    ro.observe(el);
    window.addEventListener("resize", check);
    return () => {
      ro.disconnect();
      window.removeEventListener("resize", check);
    };
  }, [overview, expanded]);

  useEffect(() => {
    let active = true;
    setOverview(null);
    setError(null);
    getRepositoryOverview(projectId)
      .then((o) => active && setOverview(o))
      .catch((e) => {
        if (!active) return;
        setError(e instanceof ApiError ? { code: e.code, message: e.message } : { message: String(e) });
      });
    return () => {
      active = false;
    };
  }, [projectId]);

  return (
    <section className="panel">
      <div className="panel__head">
        <h2>Repository</h2>
        {overview?.url && (
          <button className="ghost" onClick={() => void openExternal(overview.url!)}>
            View repository ↗
          </button>
        )}
      </div>
      {!overview && !error && <p className="muted">Loading repository…</p>}
      {error && (
        <p className="muted note">
          {error.code === "repository_unavailable"
            ? "The internal repository isn't available yet."
            : error.code === "content_too_large"
              ? "The README is too large to preview here."
              : error.message}
        </p>
      )}

      {overview && (
        <>
          <div className="repo__head">
            <span className="repo__branch">{overview.default_branch}</span>
            <span className="repo__commit">{overview.head.commit_id.slice(0, 12)}</span>
            <span className="repo__msg">{firstLine(overview.head.message)}</span>
            <span className="repo__meta muted">
              {overview.head.author} · {new Date(overview.head.committed_at).toLocaleString()}
            </span>
          </div>

          {overview.tree.length > 0 && (
            <ul className="repo__tree">
              {overview.tree.map((t) => (
                <li key={t.path} className={`repo__entry repo__entry--${t.type}`}>
                  <span className="repo__icon">{t.type === "dir" ? "📁" : "📄"}</span>
                  {t.path}
                </li>
              ))}
            </ul>
          )}

          {overview.readme_markdown ? (
            <div className="repo__readme">
              <div
                ref={bodyRef}
                className={`repo__readme-body ${expanded ? "" : "repo__readme-body--clamped"}`}
              >
                <Markdown text={overview.readme_markdown} />
              </div>
              {(overflowing || expanded) && (
                <button className="linkish repo__readme-toggle" onClick={() => setExpanded((v) => !v)}>
                  {expanded ? "Show less" : "Show full README"}
                </button>
              )}
            </div>
          ) : (
            <p className="muted note">No README in the repository root.</p>
          )}
        </>
      )}
    </section>
  );
}

function firstLine(s: string): string {
  const i = s.indexOf("\n");
  return i === -1 ? s : s.slice(0, i);
}
