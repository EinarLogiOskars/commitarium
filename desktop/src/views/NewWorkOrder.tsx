import { useState } from "react";
import { createFeature } from "../api/features";
import { ApiError } from "../api/client";
import { WORK } from "../vocab";

/** Create-a-work-order form shown in the main pane. */
export function NewWorkOrder({
  projectId,
  onCreated,
}: {
  projectId: string;
  onCreated: (featureId: string) => void;
}) {
  const [title, setTitle] = useState("");
  const [description, setDescription] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!title.trim()) return;
    setBusy(true);
    setError(null);
    try {
      const created = await createFeature(projectId, {
        title: title.trim(),
        description: description.trim(),
      });
      onCreated(created.id);
    } catch (e) {
      setError(e instanceof ApiError ? `${e.message} (${e.code})` : String(e));
    } finally {
      setBusy(false);
    }
  };

  return (
    <section className="panel">
      <h2>{WORK.newAction}</h2>
      {error && <div className="banner banner--error">{error}</div>}
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
        <button className="primary" type="submit" disabled={busy || !title.trim()}>
          {busy ? "Creating…" : "Create"}
        </button>
      </form>
    </section>
  );
}
