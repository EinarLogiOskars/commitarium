import { useEffect, useState } from "react";
import { ApiError } from "../api/client";
import {
  addTestAccount,
  copyTestAccountPassword,
  listTestAccounts,
  removeTestAccount,
  updateTestAccount,
  type TestAccount,
} from "../api/testAccounts";
import { confirmAction } from "../confirm";

// Settings → Test accounts (HELPERS_PLAN.md, H3): logins for the user's own
// app, made by Commitarium and seeded by the app in its preview. A password
// is never shown: Copy password puts it on the clipboard.
export function TestAccounts({ projectId }: { projectId: string }) {
  const [accounts, setAccounts] = useState<TestAccount[] | null>(null);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    let active = true;
    listTestAccounts(projectId)
      .then((r) => active && setAccounts(r.accounts))
      .catch((e) => active && setError(describe(e)));
    return () => {
      active = false;
    };
  }, [projectId]);

  const replace = (changed: TestAccount) =>
    setAccounts((cur) => (cur ?? []).map((a) => (a.username === changed.username ? changed : a)));

  return (
    <section className="panel">
      <h2>Test accounts</h2>
      {error && <div className="banner banner--error">{error}</div>}
      <p className="muted">
        Logins for trying your app as different people, such as an admin and a customer. Commitarium
        makes each username and password, and your app sets them up when its preview starts.
      </p>
      <p className="muted note test-accounts__where">
        These logins work only in the preview, a test copy with test data — not in your real app.
      </p>
      {accounts === null ? (
        !error && <p className="muted">Loading…</p>
      ) : accounts.length === 0 ? (
        <p className="muted note">No test accounts yet.</p>
      ) : (
        <ul className="test-accounts__list">
          {accounts.map((account) => (
            <AccountRow
              key={account.username}
              projectId={projectId}
              account={account}
              onChanged={replace}
              onRemoved={() =>
                setAccounts((cur) => (cur ?? []).filter((a) => a.username !== account.username))
              }
              onError={setError}
            />
          ))}
        </ul>
      )}
      <AddAccount
        projectId={projectId}
        onAdded={(added) => setAccounts((cur) => [...(cur ?? []), added])}
        onError={setError}
      />
    </section>
  );
}

function AccountRow({
  projectId,
  account,
  onChanged,
  onRemoved,
  onError,
}: {
  projectId: string;
  account: TestAccount;
  onChanged: (a: TestAccount) => void;
  onRemoved: () => void;
  onError: (message: string | null) => void;
}) {
  const [role, setRole] = useState(account.role);
  const [note, setNote] = useState(account.note);
  const [busy, setBusy] = useState(false);
  const [copied, setCopied] = useState(false);
  const changed = role.trim() !== account.role || note.trim() !== account.note;

  useEffect(() => {
    if (!copied) return;
    const timer = window.setTimeout(() => setCopied(false), 2000);
    return () => window.clearTimeout(timer);
  }, [copied]);

  const run = async (action: () => Promise<void>) => {
    setBusy(true);
    onError(null);
    try {
      await action();
    } catch (e) {
      onError(describe(e));
    } finally {
      setBusy(false);
    }
  };

  const save = () =>
    run(async () => {
      const updated = await updateTestAccount(projectId, account.username, {
        role: role.trim(),
        note: note.trim(),
      });
      setRole(updated.role);
      setNote(updated.note);
      onChanged(updated);
    });

  const copy = () =>
    run(async () => {
      await copyTestAccountPassword(projectId, account.username);
      setCopied(true);
    });

  const remove = async () => {
    const sure = await confirmAction(
      `Remove the test account “${account.label}”? It stops working in the preview the next time it starts.`,
      "Remove",
    );
    if (sure) await run(() => removeTestAccount(projectId, account.username).then(onRemoved));
  };

  return (
    <li className="test-accounts__row">
      <div className="test-accounts__who">
        <span className="test-accounts__label">{account.label}</span>
        <span className="test-accounts__username">{account.username}</span>
      </div>
      <form
        className="test-accounts__fields"
        onSubmit={(e) => {
          e.preventDefault();
          if (changed) void save();
        }}
      >
        <input
          value={role}
          placeholder="Role"
          aria-label={`Role of ${account.label}`}
          maxLength={40}
          onChange={(e) => setRole(e.target.value)}
          disabled={busy}
        />
        <input
          value={note}
          placeholder="Note"
          aria-label={`Note on ${account.label}`}
          maxLength={300}
          onChange={(e) => setNote(e.target.value)}
          disabled={busy}
        />
        {changed && (
          <button className="primary" type="submit" disabled={busy}>
            Save
          </button>
        )}
      </form>
      <div className="test-accounts__actions">
        <button className="ghost" onClick={() => void copy()} disabled={busy}>
          {copied ? "Copied ✓" : "Copy password"}
        </button>
        <button className="ghost danger" onClick={() => void remove()} disabled={busy}>
          Remove
        </button>
      </div>
    </li>
  );
}

function AddAccount({
  projectId,
  onAdded,
  onError,
}: {
  projectId: string;
  onAdded: (a: TestAccount) => void;
  onError: (message: string | null) => void;
}) {
  const [label, setLabel] = useState("");
  const [role, setRole] = useState("");
  const [note, setNote] = useState("");
  const [busy, setBusy] = useState(false);

  const add = async () => {
    setBusy(true);
    onError(null);
    try {
      onAdded(
        await addTestAccount(projectId, {
          label: label.trim(),
          role: role.trim(),
          note: note.trim(),
        }),
      );
      setLabel("");
      setRole("");
      setNote("");
    } catch (e) {
      onError(describe(e));
    } finally {
      setBusy(false);
    }
  };

  return (
    <form
      className="settings-row test-accounts__add"
      onSubmit={(e) => {
        e.preventDefault();
        if (label.trim()) void add();
      }}
    >
      <label>
        Name
        <input
          value={label}
          placeholder="e.g. Admin"
          maxLength={60}
          onChange={(e) => setLabel(e.target.value)}
          disabled={busy}
        />
      </label>
      <label>
        Role in your app
        <input
          value={role}
          placeholder="e.g. admin"
          maxLength={40}
          onChange={(e) => setRole(e.target.value)}
          disabled={busy}
        />
      </label>
      <label>
        Note
        <input
          value={note}
          placeholder="Optional"
          maxLength={300}
          onChange={(e) => setNote(e.target.value)}
          disabled={busy}
        />
      </label>
      <button className="primary" type="submit" disabled={busy || !label.trim()}>
        {busy ? "Adding…" : "Add test account"}
      </button>
    </form>
  );
}

function describe(e: unknown): string {
  if (e instanceof ApiError) return e.message;
  if (e instanceof Error) return e.message;
  return String(e);
}
