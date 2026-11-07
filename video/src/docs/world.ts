// The demo world the docs screenshots show: one person's Commitarium with
// Plant Water Tracker, two agents, and a server. The app's own API code runs
// unchanged; only the transport (Tauri's invoke and fetch) answers from here.
// A request nothing here answers is logged, so a new screen's needs show up.

import captured from "./captured.json";
import { BOOKS, PLANTS, RECIPES, booksChat, booksGoal, newAppChat, projects, recipesChat, signInChat } from "./demo";

/** Switches a screen sets before it renders: one world, a few states. */
export const state = { signIn: false, authentik: false, server: false };

const now = "2026-11-03T12:00:00Z";
const later = (hours: number) => new Date(Date.parse(now) + hours * 3600_000).toISOString();

const agents = [
  { id: "claude", name: "Claude", provider: "claude", created_at: now, updated_at: now },
  { id: "codex", name: "Codex", provider: "codex", created_at: now, updated_at: now },
];

const models = (provider: string) =>
  provider === "claude"
    ? [
        { id: "claude-opus-5-5", display_name: "Claude Opus 5.5" },
        { id: "claude-sonnet-5-5", display_name: "Claude Sonnet 5.5" },
        { id: "claude-haiku-5-5", display_name: "Claude Haiku 5.5" },
      ]
    : [
        { id: "gpt-6.1-sol", display_name: "GPT-6.1-Sol" },
        { id: "gpt-6.1-terra", display_name: "GPT-6.1 Terra" },
        { id: "gpt-6-luna", display_name: "GPT-6-Luna" },
      ];

const catalogs = ["claude", "codex"].flatMap((provider) =>
  ["lead", "reviewer"].map((role) => ({ provider, role, models: models(provider), fetched_at: now })),
);

/** Answers by method and path; a function gets the request body. */
type Answer = unknown | ((body: unknown, url: URL) => unknown);
const coordinator: [RegExp, Answer][] = [
  [/^GET \/health$/, { status: "ok" }],
  [/^GET \/api\/v1\/agents$/, agents],
  [/^GET \/api\/v1\/agents\/limits$/, { limits: [] }],
  [
    /^GET \/api\/v1\/agents\/usage$/,
    {
      agents: [
        {
          agent: "claude",
          observed_at: now,
          windows: [
            { name: "5 hours", used_percent: 34, resets_at: later(3) },
            { name: "Week", used_percent: 18, resets_at: later(90) },
          ],
        },
        {
          agent: "codex",
          observed_at: now,
          windows: [
            { name: "5 hours", used_percent: 12, resets_at: later(4) },
            { name: "Week", used_percent: 9, resets_at: later(120) },
          ],
        },
      ],
    },
  ],
  [/^GET \/api\/v1\/models$/, { catalogs }],
  [/^POST \/api\/v1\/models\/refresh$/, { catalogs }],
  [/^GET \/api\/v1\/projects$/, projects],
  [
    /^GET \/api\/v1\/model-guide$/,
    {
      as_of: "2026-11-01",
      models: [
        { id: "claude-opus-5-5", tier: "frontier", source: "official", note: "Planning and hard problems" },
        { id: "claude-sonnet-5-5", tier: "strong", source: "official" },
        { id: "claude-haiku-5-5", tier: "light", source: "official" },
        { id: "gpt-6.1-sol", tier: "strong", source: "official" },
        { id: "gpt-6.1-terra", tier: "light", source: "official" },
        { id: "gpt-6-luna", tier: "avoid", source: "official" },
      ],
    },
  ],
  [/^GET \/api\/v1\/assistant\/conversations\/commitarium$/, newAppChat],
  [/^GET \/api\/v1\/assistant\/autopilot$/, { goals: { [BOOKS]: booksGoal }, launches: { [BOOKS]: "building" } }],
  [/^GET \/api\/v1\/attention$/, { generated_at: now, items: [], running: [] }],
  [/^GET \/api\/v1\/marketplace\/installs$/, { installs: [] }],
  [/^GET \/api\/v1\/assistant\/conversations\/prj_bookclub(\/chats\/[a-z_]+)?$/, booksChat],
  [/^GET \/api\/v1\/assistant\/conversations\/prj_recipebox(\/chats\/[a-z_]+)?$/, recipesChat],
  [/^GET \/api\/v1\/assistant\/conversations\/prj_plantwatertracker\/chats\/chat_signin$/, signInChat],
  [/^GET \/api\/v1\/assistant\/conversations\/[a-z_]+\/chats$/, { chats: [] }],
  [/^GET \/api\/v1\/projects\/prj_recipebox$/, projects[2]],
  [/^GET \/api\/v1\/projects\/prj_bookclub$/, projects[1]],
  [/^GET \/api\/v1\/projects\/(prj_recipebox|prj_bookclub)\/(features|documents)$/, []],
  [/^GET \/api\/v1\/projects\/(prj_recipebox|prj_bookclub)\/agents-at-work$/, { agents: [] }],
  [
    /^GET \/api\/v1\/projects\/[a-z_]+\/linked-repository$/,
    {
      url: "https://github.com/anna/plant-water-tracker.git",
      branch: "main",
      local_branch: "linked/main",
      has_token: true,
      pulled_at: now,
      pushed_at: now,
      push_branch: "commitarium",
    },
  ],
  [/^GET \/api\/v1\/projects\/[a-z_]+\/features\/[A-Za-z0-9_]+\/events\/stream$/, ""],
  [/^GET \/api\/v1\/marketplace$/, { url: "https://market.commitarium.com", account: null }],
  [
    /^GET \/api\/v1\/assistant\/settings$/,
    { agent: "claude", model: "claude-opus-5-5", ask_before: [], max_effort: "high" },
  ],
];

const unix = (minutes: number) => Math.round(Date.parse(now) / 1000) - minutes * 60;

const host: [RegExp, Answer][] = [
  [/^GET \/v1\/host\/update$/, { current: "0.11.0", latest: "0.11.0", available: false }],
  [
    /^GET \/v1\/devices$/,
    [
      { fingerprint: "a1", name: "Anna's MacBook Pro", role: "owner", paired_at: unix(60 * 24 * 12) },
      { fingerprint: "b2", name: "Office iMac", role: "device", paired_at: unix(60 * 24 * 3) },
    ],
  ],
  [
    /^GET \/v1\/disk\/space$/,
    { path: "/var/lib/commitarium", available_bytes: 21e9, recommended_free_bytes: 5e9, backup_ready: true },
  ],
  [/^GET \/v1\/backups\/underway$/, { underway: null }],
  [
    /^GET \/v1\/backups$/,
    [0, 1, 2, 3].map((night) => ({
      name: `backup-${unix(night * 1440 + 180)}`,
      created_at_unix_seconds: unix(night * 1440 + 180),
      app_version: "0.11.0",
      size: [212e6, 209e6, 204e6, 198e6][night],
    })),
  ],
  [
    /^GET \/v1\/deployments$/,
    () => [
      {
        project_id: PLANTS,
        current: { commit_id: "aaf2585", deployed_at: unix(90) },
        history: [
          { commit_id: "aaf2585", deployed_at: unix(90) },
          { commit_id: "1b1e55f", deployed_at: unix(60 * 26) },
        ],
        status: {
          projectId: PLANTS,
          target: { kind: "deployment", commit_id: "aaf2585" },
          commitId: "aaf2585",
          state: "running",
          urls: [{ service: "web", url: "http://127.0.0.1:32778", open: true }],
          error: null,
        },
      },
    ],
  ],
  [
    /^GET \/v1\/publishing$/,
    () => ({
      domain: "example.com",
      server_address: "203.0.113.10",
      apps: [
        {
          project_id: PLANTS,
          name: "plants",
          access: state.signIn ? "company" : "private",
          url: "https://plants.example.com",
          live: true,
          groups: state.signIn ? ["gardeners"] : [],
        },
      ],
      sign_in: state.signIn
        ? {
            issuer: state.authentik ? "https://auth.example.com/application/o/commitarium/" : "https://accounts.google.com",
            client_id: "commitarium",
            scopes: "openid email profile",
            groups_claim: "groups",
            redirect_url: "https://sign-in.example.com/oauth2/callback",
            sign_out_url: "https://sign-in.example.com/sign-out",
          }
        : null,
      authentik: {
        installed: state.authentik,
        running: state.authentik,
        admin_url: state.authentik ? "https://auth.example.com/if/admin/" : null,
        admin_user: "akadmin",
        memory_total_bytes: 16 * 1024 ** 3,
        memory_needed_gb: 3,
      },
    }),
  ],
  [/^GET \/v1\/version$/, { version: "0.11.0", api_version: 1 }],
  [
    /^GET \/v1\/stack\/status$/,
    ["coordinator", "forgejo", "simulated-codex-worker", "codex-worker", "claude-worker"].map((service) => ({
      service,
      state: "running",
      health: "healthy",
      status: "Up 2 hours",
    })),
  ],
  [
    /^GET \/v1\/docker\/probe$/,
    {
      docker_installed: true,
      docker_running: true,
      compose_available: true,
      docker_launchable: true,
      docker_version: "29.0.1",
      compose_version: "2.40.0",
      install_url: "https://docs.docker.com/get-docker/",
    },
  ],
];

const commands: Record<string, Answer> = {
  list_profiles: [
    { id: "claude", name: "Claude", provider: "claude", status: "connected" },
    { id: "codex", name: "Codex", provider: "codex", status: "connected" },
  ],
  host_connection: { url: "http://host.demo", token: "demo" },
  load_desktop_settings: {
    notifications_configured: true,
    notifications_enabled: true,
    notify_attention: true,
    notify_failures: true,
    notify_auto_merges: false,
    exit_behavior: "keep_running",
  },
  load_ui_state: null,
  save_ui_state: null,
  places: () => ({
    servers: [{ fingerprint: "demo", address: "203.0.113.10:7443", name: "Office server" }],
    active: state.server ? "demo" : null,
  }),
  connection_mode: () => ({ mode: state.server ? "remote" : "local", server: state.server ? "203.0.113.10:7443" : null }),
  check_desktop_update: { status: "up_to_date", current_version: "0.11.0" },
  stack_start: null,
  exit_prompt_shown: null,
  coordinator_freshness: "fresh",
  webview_snapshot_supported: false,
  prune_session_previews: null,
  get_project_sync_state: (args: unknown) => ({
    projectId: (args as { projectId?: string })?.projectId ?? RECIPES,
    source: { sourceType: "git", path: "/Users/anna/code/recipe-box", createdByCommitarium: false },
    canonical: { defaultBranch: "main", headCommitId: "4be91a0" },
    local: {
      watermarkCommitId: "2c71d3e",
      localCommitId: "2c71d3e",
      unsyncedFeatures: [
        { featureId: "fea_tests", title: "Add API and form tests", baseCommitId: "2c71d3e", mergeCommitId: "9a01f22", mergedAt: now },
        { featureId: "fea_search", title: "Search ingredients and tags", baseCommitId: "9a01f22", mergeCommitId: "4be91a0", mergedAt: now },
      ],
      localCommits: 0,
      uncommittedFiles: 0,
    },
    upstreams: [],
  }),
  get_preview_status: (args: unknown) => ({
    projectId: (args as { projectId?: string })?.projectId ?? PLANTS,
    target: { kind: "canonical" },
    commitId: "aaf2585",
    state: (args as { projectId?: string })?.projectId === BOOKS ? "starting" : "running",
    urls: [{ service: "frontend", url: "http://127.0.0.1:5173", open: true }],
    error: null,
  }),
  agent_cli_status: [
    { provider: "codex", installed: "0.166.0", target: null, state: "current", checked: true },
    { provider: "claude", installed: "2.1.317", target: null, state: "current", checked: true },
  ],
  coordinator_connection: { url: "http://coordinator.demo", token: null },
};

/** What nothing answered, shown on a screenshot rendered with debug. */
export const missed = new Set<string>();
function miss(what: string) {
  missed.add(what);
}

export async function invokeDemo(command: string, args?: Record<string, unknown>): Promise<unknown> {
  if (command in commands) {
    const answer = commands[command];
    return typeof answer === "function" ? answer(args, new URL("demo:")) : answer;
  }
  miss(`invoke ${command}`);
  return null;
}

export async function fetchDemo(input: string, init?: RequestInit): Promise<Response> {
  const url = new URL(input, "http://coordinator.demo");
  const method = (init?.method ?? "GET").toUpperCase();
  const key = `${method} ${url.pathname}`;
  const routes = url.host === "host.demo" ? host : coordinator;
  // A coordinator's real answer, recorded (capture.mjs), comes first.
  const recorded = (captured as Record<string, unknown>)[url.pathname];
  if (method === "GET" && url.host !== "host.demo" && recorded !== undefined) {
    return new Response(JSON.stringify(recorded), { status: 200, headers: { "Content-Type": "application/json" } });
  }
  const body = typeof init?.body === "string" ? JSON.parse(init.body || "null") : null;
  for (const [pattern, answer] of routes) {
    if (pattern.test(key)) {
      const value = typeof answer === "function" ? answer(body, url) : answer;
      return new Response(JSON.stringify(value), { status: 200, headers: { "Content-Type": "application/json" } });
    }
  }
  miss(`${url.host} ${key}`);
  return new Response(JSON.stringify({ error: { code: "not_found", message: "not in the demo world" } }), {
    status: 404,
    headers: { "Content-Type": "application/json" },
  });
}
