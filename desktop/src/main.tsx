import React from "react";
import ReactDOM from "react-dom/client";
import App from "./App";
import { AppWindow } from "./views/AppWindow";
import { applyTheme, readTheme } from "./settings/theme";

applyTheme(readTheme());

// An app's own window (APPS_PLAN.md) is told which app it shows.
const appProject = (window as { __COMMITARIUM_APP__?: string }).__COMMITARIUM_APP__;

ReactDOM.createRoot(document.getElementById("root") as HTMLElement).render(
  <React.StrictMode>
    {appProject ? <AppWindow projectId={appProject} /> : <App />}
  </React.StrictMode>,
);
