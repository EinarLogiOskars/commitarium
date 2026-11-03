import App from "../../../desktop/src/App";
import { connectBridge } from "../../../desktop/src/api/client";
import { setServer } from "../../../desktop/src/connection";
import { DEV_MODE_KEY } from "../../../desktop/src/devMode";
import { state } from "./world";

/** How the whole app is set up for a screenshot. */
export interface Setup {
  /** Developer mode on. */
  dev?: boolean;
  area?: "apps" | "projects" | "marketplace";
  /** Shown on the demo server rather than this computer. */
  server?: boolean;
  /** The project selected in Developer mode, and its chat. */
  project?: string;
  chat?: string;
  /** Company sign-in connected, and through authentik on the server. */
  signIn?: boolean;
  authentik?: boolean;
}

// The whole app as it opens, set up through the same storage the app reads:
// startup takes its quick path, as just after switching to a running side.
export function AppScreen({
  dev = false,
  area = "apps",
  server = false,
  project,
  chat,
  signIn = false,
  authentik = false,
}: Setup) {
  state.signIn = signIn;
  state.authentik = authentik;
  state.server = server;
  try {
    localStorage.setItem("commitarium.chats.selected", JSON.stringify(project ? { scope: project, chat } : null));
    localStorage.setItem(DEV_MODE_KEY, dev ? "1" : "0");
    localStorage.setItem("commitarium.area", area);
    sessionStorage.setItem("commitarium.switched", "1");
  } catch {
    // A still without storage shows the defaults.
  }
  setServer(server ? "203.0.113.10:7443" : null);
  if (server) connectBridge("http://coordinator.demo", "demo");
  return <App />;
}
