import React from "react";
import ReactDOM from "react-dom/client";
import App from "./App";
import { AppWindow } from "./views/AppWindow";
import { StartChoice } from "./views/StartChoice";
import { connectBridge } from "./api/client";
import { setServer } from "./connection";
import { bridgeConnection, connectionMode, coordinatorConnection } from "./ipc";
import { applyTheme, readTheme } from "./settings/theme";

applyTheme(readTheme());

// An app's own window (APPS_PLAN.md) is told which app it shows, and whether
// it shows the app's preview rather than the real app (HELPERS_PLAN.md H5).
const told = window as { __COMMITARIUM_APP__?: string; __COMMITARIUM_PREVIEW__?: boolean };
const appProject = told.__COMMITARIUM_APP__;
const appPreview = told.__COMMITARIUM_PREVIEW__ === true;

const root = ReactDOM.createRoot(document.getElementById("root") as HTMLElement);
const show = (page: React.ReactNode) => root.render(<React.StrictMode>{page}</React.StrictMode>);

async function start() {
  if (appProject) {
    const connection = await coordinatorConnection().catch(() => null);
    if (connection) connectBridge(connection.coordinator_url, connection.token);
    show(<AppWindow projectId={appProject} preview={appPreview} />);
    return;
  }
  const { mode, server } = await connectionMode().catch(() => ({ mode: "local", server: null }));
  if (mode === null) {
    show(<StartChoice onLocal={() => show(<App />)} />);
    return;
  }
  if (mode === "remote") {
    setServer(server);
    await bridgeConnection()
      .then(({ coordinator_url, token }) => {
        if (coordinator_url) connectBridge(coordinator_url, token);
      })
      .catch(() => {}); // The boot screen says so and offers a way out.
  }
  show(<App />);
}

void start();
