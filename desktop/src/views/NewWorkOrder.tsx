import { useState } from "react";
import { createFeature } from "../api/features";
import { ApiError } from "../api/client";
import { WORK } from "../vocab";
import type { Project } from "../api/types";

/** Create-a-work-order form: what and why. Agents, models, and workflow
 * settings are chosen at Start. */
/** A prefilled work order, optionally with lighter settings than the project's. */
export interface OrderDraft {
  title: string;
  description: string;
  independentTests?: boolean;
}

export function NewWorkOrder({
  project,
  initial,
  onCreated,
}: {
  project: Project;
  /** Prefill for a work order started from elsewhere (e.g. preview setup). */
  initial?: OrderDraft;
  /** Kept for callers; validation is now offered when choosing Start settings. */
  onOpenValidation?: () => void;
  onCreated: (featureId: string) => void;
}) {
  const [title, setTitle] = useState(initial?.title ?? "");
  const [description, setDescription] = useState(initial?.description ?? "");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!title.trim()) return;
    setBusy(true);
    setError(null);
    try {
      const created = await createFeature(project.id, {
        title: title.trim(),
        description: description.trim(),
        // Agents, models, and workflow settings are chosen at Start; a draft
        // carries the project's defaults until then.
        ...(initial?.independentTests !== undefined
          ? { independent_acceptance_tests: initial.independentTests }
          : {}),
      });
      onCreated(created.id);
    } catch (e) {
      if (e instanceof ApiError && e.code === "project_toolchain_required") {
        setError(
          "This project needs a stack before you can create work orders. Open Stack to choose one.",
        );
      } else {
        setError(e instanceof ApiError ? `${e.message} (${e.code})` : String(e));
      }
    } finally {
      setBusy(false);
    }
  };

  return (
    <section className="panel">
      <h2>{WORK.newAction}</h2>
      {error && <div className="banner banner--error">{error}</div>}
      <RoundsNotice />
      <form className="create create--feature" onSubmit={submit}>
        <input
          type="text"
          placeholder={`${WORK.Singular} title`}
          value={title}
          onChange={(e) => setTitle(e.target.value)}
          disabled={busy}
          autoFocus
        />
        <textarea
          placeholder="Description (optional)"
          value={description}
          onChange={(e) => setDescription(e.target.value)}
          disabled={busy}
          rows={3}
        />

        <p className="muted">
          Agents, models, and how far it runs on its own are chosen when you start it.
        </p>

        <button className="primary" type="submit" disabled={busy || !title.trim()}>
          {busy ? "Creating…" : "Create"}
        </button>
      </form>
    </section>
  );
}

const ROUNDS_NOTICE_KEY = "commitarium.roundsNoticeDismissed";

// Shown until dismissed once: the round cap is fixed, so the user only needs
// to learn it, not configure it.
function RoundsNotice() {
  const [dismissed, setDismissed] = useState(() => {
    try {
      return localStorage.getItem(ROUNDS_NOTICE_KEY) === "1";
    } catch {
      return false;
    }
  });
  if (dismissed) return null;
  const dismiss = () => {
    try {
      localStorage.setItem(ROUNDS_NOTICE_KEY, "1");
    } catch {
      // Without storage the notice simply shows again next time.
    }
    setDismissed(true);
  };
  return (
    <div className="banner banner--info rounds-notice">
      <span>
        The agents get up to 10 rounds to agree on a plan and 10 rounds of review. If they
        haven&apos;t settled by then, the work order pauses and asks you how to continue.
      </span>
      <button type="button" className="ghost" onClick={dismiss}>
        Got it
      </button>
    </div>
  );
}
