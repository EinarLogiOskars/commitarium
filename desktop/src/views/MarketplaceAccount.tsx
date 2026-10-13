import { useCallback, useEffect, useState } from "react";
import { ApiError } from "../api/client";
import {
  approveMarketplaceUser,
  declineMarketplaceUser,
  getMarketplace,
  listPendingMarketplaceUsers,
  signInToMarketplace,
  signOutOfMarketplace,
  signUpToMarketplace,
  type MarketplaceStatus,
  type PendingMarketplaceUser,
} from "../api/marketplace";

type Mode = "sign-in" | "sign-up" | "requested" | "pending";

// The user's marketplace account (MARKETPLACE_PLAN.md): create one, which
// waits for the admin's approval, sign in, and sign out. The admin also
// approves new accounts here. Hidden when the coordinator has no marketplace.
export function MarketplaceAccountSection() {
  const [status, setStatus] = useState<MarketplaceStatus | null>(null);
  const [mode, setMode] = useState<Mode>("sign-in");
  const [username, setUsername] = useState("");
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    getMarketplace()
      .then(setStatus)
      .catch(() => setStatus(null));
  }, []);

  if (!status) return null;

  const act = async (action: () => Promise<void>) => {
    setBusy(true);
    setError(null);
    try {
      await action();
    } catch (e) {
      if (e instanceof ApiError && e.code === "marketplace_pending_approval") setMode("pending");
      else setError(describe(e));
    } finally {
      setBusy(false);
    }
  };

  const signIn = () =>
    act(async () => {
      setStatus(await signInToMarketplace(username.trim(), password));
      setPassword("");
    });
  const signUp = () =>
    act(async () => {
      await signUpToMarketplace(username.trim(), email.trim(), password);
      setPassword("");
      setMode("requested");
    });
  const signOut = () =>
    act(async () => {
      await signOutOfMarketplace();
      setStatus({ ...status, account: null });
      setMode("sign-in");
    });

  const account = status.account;
  return (
    <section className="set__section">
      <h3 className="set__heading">Marketplace</h3>
      {error && <div className="banner banner--error">{error}</div>}

      {account ? (
        <>
          <div className="set__fact">
            <span>
              Signed in as <strong>{account.username}</strong>
              {account.admin && <span className="muted"> · admin</span>}
            </span>
            <button className="ghost" disabled={busy} onClick={() => void signOut()}>
              Sign out
            </button>
          </div>
          <p className="muted set__note">
            Your apps can be released to the marketplace and you can install others&apos; apps.
          </p>
          {account.admin && <PendingAccounts />}
        </>
      ) : mode === "requested" || mode === "pending" ? (
        <>
          <p className="set__note">
            {mode === "requested"
              ? "Your account was requested. You can sign in once it is approved."
              : "Your account is still waiting for approval."}
          </p>
          <div className="row">
            <button disabled={busy} onClick={() => setMode("sign-in")}>
              Sign in
            </button>
          </div>
        </>
      ) : (
        <>
          <p className="muted set__note">
            {mode === "sign-in"
              ? "Sign in to release your apps and install apps others made."
              : "Choose a username and password. Your account is ready once it is approved."}
          </p>
          <div className="import-form">
            <label>
              Username
              <input
                value={username}
                autoComplete="username"
                onChange={(e) => setUsername(e.target.value)}
                disabled={busy}
              />
            </label>
            {mode === "sign-up" && (
              <label>
                Email
                <input
                  type="email"
                  value={email}
                  autoComplete="email"
                  onChange={(e) => setEmail(e.target.value)}
                  disabled={busy}
                />
              </label>
            )}
            <label>
              Password
              <input
                type="password"
                value={password}
                autoComplete={mode === "sign-in" ? "current-password" : "new-password"}
                onChange={(e) => setPassword(e.target.value)}
                disabled={busy}
              />
              {mode === "sign-up" && (
                <span className={password && password.length < 8 ? "set__warn" : undefined}>
                  At least 8 characters
                  {password && password.length < 8 ? ` (${8 - password.length} more)` : ""}
                </span>
              )}
            </label>
          </div>
          <div className="row">
            {mode === "sign-in" ? (
              <>
                <button
                  className="primary"
                  disabled={busy || !username.trim() || !password}
                  onClick={() => void signIn()}
                >
                  {busy ? "Signing in…" : "Sign in"}
                </button>
                <button className="ghost" disabled={busy} onClick={() => setMode("sign-up")}>
                  Create an account
                </button>
              </>
            ) : (
              <>
                <button
                  className="primary"
                  disabled={busy || !username.trim() || !email.trim() || password.length < 8}
                  onClick={() => void signUp()}
                >
                  {busy ? "Requesting…" : "Request account"}
                </button>
                <button className="ghost" disabled={busy} onClick={() => setMode("sign-in")}>
                  I have an account
                </button>
              </>
            )}
          </div>
        </>
      )}
    </section>
  );
}

// Accounts waiting for the admin, each approved or declined in one click.
function PendingAccounts() {
  const [users, setUsers] = useState<PendingMarketplaceUser[] | null>(null);
  const [busy, setBusy] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);

  const load = useCallback(() => {
    listPendingMarketplaceUsers()
      .then(setUsers)
      .catch((e) => setError(describe(e)));
  }, []);
  useEffect(load, [load]);

  const decide = async (username: string, approve: boolean) => {
    setBusy(username);
    setError(null);
    try {
      await (approve ? approveMarketplaceUser(username) : declineMarketplaceUser(username));
      setUsers((current) => current?.filter((u) => u.username !== username) ?? null);
    } catch (e) {
      setError(describe(e));
    } finally {
      setBusy(null);
    }
  };

  return (
    <>
      <h3 className="set__heading">Account requests</h3>
      {error && <div className="banner banner--error">{error}</div>}
      {users === null ? null : users.length === 0 ? (
        <p className="muted set__note">No one is waiting.</p>
      ) : (
        users.map((user) => (
          <div key={user.username} className="set__fact">
            <span>
              <strong>{user.username}</strong>{" "}
              <span className="muted">
                {user.email} · {new Date(user.requested_at).toLocaleDateString()}
              </span>
            </span>
            <span className="row">
              <button
                className="primary"
                disabled={busy !== null}
                onClick={() => void decide(user.username, true)}
              >
                Approve
              </button>
              <button
                className="ghost"
                disabled={busy !== null}
                onClick={() => void decide(user.username, false)}
              >
                Decline
              </button>
            </span>
          </div>
        ))
      )}
    </>
  );
}

function describe(e: unknown): string {
  if (e instanceof ApiError) return e.message;
  return String(e);
}
