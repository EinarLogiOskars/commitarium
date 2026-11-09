import { request } from "./client";

// A project's test logins (HELPERS_PLAN.md, H3): logins for the user's own
// app that work only in its preview. Commitarium makes each username and
// password; the list never carries the passwords, and copying one fetches it
// straight onto the clipboard without showing it.

export interface TestAccount {
  label: string;
  username: string;
  role: string;
  note: string;
  created_at: string;
}

const accountsPath = (id: string) => `/api/v1/projects/${encodeURIComponent(id)}/test-accounts`;
const accountPath = (id: string, username: string) =>
  `${accountsPath(id)}/${encodeURIComponent(username)}`;

export const listTestAccounts = (id: string): Promise<{ accounts: TestAccount[] }> =>
  request(accountsPath(id));

export const addTestAccount = (
  id: string,
  account: { label: string; role: string; note: string },
): Promise<TestAccount> => request(accountsPath(id), { method: "POST", body: account });

export const updateTestAccount = (
  id: string,
  username: string,
  change: { role?: string; note?: string },
): Promise<TestAccount> => request(accountPath(id, username), { method: "PATCH", body: change });

export const removeTestAccount = (id: string, username: string): Promise<void> =>
  request(accountPath(id, username), { method: "DELETE" });

const fetchPassword = (id: string, username: string): Promise<string> =>
  request<{ password: string }>(`${accountPath(id, username)}/password`).then((r) => r.password);

/** Puts a login's password on the clipboard. WebKit allows a clipboard
 * write only within the click, so the password goes in as a promise the
 * write waits for; where that is not supported, it is fetched first. */
export async function copyTestAccountPassword(id: string, username: string): Promise<void> {
  if (typeof ClipboardItem !== "undefined" && navigator.clipboard?.write) {
    try {
      const blob = fetchPassword(id, username).then((p) => new Blob([p], { type: "text/plain" }));
      await navigator.clipboard.write([new ClipboardItem({ "text/plain": blob })]);
      return;
    } catch {
      // Fall through to the plain write below.
    }
  }
  await navigator.clipboard.writeText(await fetchPassword(id, username));
}
