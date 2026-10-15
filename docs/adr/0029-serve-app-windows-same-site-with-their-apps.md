# ADR-029: Serve app windows same-site with their apps

- Status: Accepted (amends the app window of [ADR-024](0024-in-an-app-the-assistant-drives.md))
- Date: 2026-10-15

## Context

An app's window loads Commitarium's own page (`tauri://localhost`), which
frames the running app (`http://127.0.0.1:<port>`) and floats the assistant
over it. To the browser engine the app is a cross-site frame. WebKit, which
runs the window on macOS, blocks third-party cookies outright and partitions
third-party storage; WebView2 on Windows sends cross-site cookies only when
they are `SameSite=None; Secure`, which plain-http previews cannot be. So an
app's sign-in never sticks: a tester's imported app showed its login form
again after every login, and the agent worked around it inside the app with
an "open in your browser" link.

Making the app the window's own page fixes sign-in but loses the assistant
over it; two webviews in one window keep both but need Tauri's unstable
multi-webview API.

## Decision

The app window's page is served from `127.0.0.1` as well, on another port.
Cookies and storage go by site, which ignores the port, so the framed app is
same-site with the page and its sign-in works, while the page and the app
stay different origins: the app cannot read the page or call its commands.
Everything else about the window stays: the frame, the floating assistant,
the building and failure screens.

- **Serving.** In a release, a small server on a random free `127.0.0.1` port
  serves the bundled frontend and nothing else (`app_pages.rs`): GET and HEAD
  only, only for its own `Host` (no name rebound to 127.0.0.1), never framed
  (`frame-ancestors 'none'`), never cached. In development Vite serves the
  page and listens on `127.0.0.1` for it.
- **Least privilege.** The page is a remote origin to Tauri, which checks every
  command it calls. A capability added at runtime grants that exact origin,
  port included, only what an app window needs: its preview, the assistant's
  agent sign-in, and `stack_up`, plus the coordinator through the HTTP
  plugin. Docker control, backups, imports, publishing, and updates stay with
  the main window. Granting app commands one by one takes an app manifest, so
  the main window now lists its commands too.
- **The frame** is sandboxed without top navigation, so an app cannot navigate
  its window away.

## Consequences

- Apps that sign in work in their window, on macOS and Windows alike, with no
  change to the apps.
- The app window's page answers on a loopback port while Commitarium runs. It
  serves only the frontend's public files; any process able to take that port
  first already runs as the user.
- A new Tauri command must be added to `lib.rs`, `build.rs`, and the
  `main-window` set (a test checks they agree), and to `app-page` only if the
  app window uses it.
- Over plain http, WebKit drops cookies marked `Secure` (and so every
  `SameSite=None` cookie), even on 127.0.0.1 and even with no frame; Chrome
  makes an exception for loopback, so such an app works in a browser and not
  here. Checked with WKWebView: `SameSite=Lax` and plain `SameSite=None`
  cookies stick in the same-site frame, `Secure; SameSite=None` ones nowhere.
  Agents are told to keep sign-in cookies on their framework's http defaults
  (the preview conventions every lead gets, and the preview setup rules).
- The workaround agents wrote into apps ("open the app in your browser to sign
  in") is no longer needed and can be removed from those apps.
