// The demo world's people, apps and conversations, kept apart from the
// routing in world.ts so a new screen only adds data here.

export const NOW = "2026-11-03T12:00:00Z";
export const ago = (minutes: number) => new Date(Date.parse(NOW) - minutes * 60_000).toISOString();

export const PLANTS = "prj_plantwatertracker";
export const BOOKS = "prj_bookclub";
export const RECIPES = "prj_recipebox";

export const projects = [
  {
    id: PLANTS,
    name: "Plant Water Tracker",
    recovery_policy: "approval_required",
    merge_policy: "auto_after_gates",
    autonomy_policy: "run_to_completion",
    app: true,
    assistant_mode: "builds",
    dialogue_limits: { planning_rounds: 10, implementation_review_rounds: 10 },
    forgejo_repository: { owner: "commitarium_admin", name: "plant-water-tracker", default_branch: "main", bound_at: ago(300) },
    repository_status: "ready",
    created_at: ago(300),
  },
  {
    id: BOOKS,
    name: "Book Club",
    recovery_policy: "approval_required",
    merge_policy: "auto_after_gates",
    autonomy_policy: "run_to_completion",
    app: true,
    assistant_mode: "builds",
    dialogue_limits: { planning_rounds: 10, implementation_review_rounds: 10 },
    forgejo_repository: { owner: "commitarium_admin", name: "book-club", default_branch: "main", bound_at: ago(40) },
    repository_status: "ready",
    created_at: ago(40),
  },
  {
    id: RECIPES,
    name: "recipe-box",
    recovery_policy: "approval_required",
    merge_policy: "user_approval",
    autonomy_policy: "phase_checkpoints",
    app: false,
    assistant_mode: "assists",
    dialogue_limits: { planning_rounds: 10, implementation_review_rounds: 10 },
    forgejo_repository: { owner: "commitarium_admin", name: "recipe-box", default_branch: "main", bound_at: ago(20) },
    repository_status: "ready",
    created_at: ago(20),
  },
];

let id = 0;
const message = (role: "user" | "assistant" | "event", text: string, minutes: number) => ({
  id: `msg_${++id}`,
  role,
  text,
  occurred_at: ago(minutes),
});

/** An action card, with the fields every card has. */
export const action = (fields: Record<string, unknown>) => ({
  reason: "",
  title: "",
  description: "",
  vision_item_id: "",
  steps: "",
  independent_tests: false,
  lead_agent: "",
  lead_model: "",
  reviewer_agent: "",
  reviewer_model: "",
  work_order_id: "",
  when: "",
  status: "proposed",
  ...fields,
});

const ask = message("user", "I need an app to track when my plants need water", 6);
const reply = message(
  "assistant",
  "That's a clear one, so I'll build it with sensible defaults. Each plant gets how often it needs water, " +
    "the home screen shows what's due, overdue and coming up, and one tap logs a watering. My guesses are on " +
    "the card: tell me if something should be different before you create it.",
  5,
);

/** The top-level chat where New app starts. */
export const newAppChat = {
  scope: "commitarium",
  chat: "chat_newapp",
  title: "A plant watering app",
  status: "idle",
  agent: "claude",
  model: "claude-opus-5-5",
  notes: "",
  updated_at: ago(5),
  messages: [ask, reply],
  actions: [
    action({
      id: "act_newapp",
      message_id: reply.id,
      type: "create_app",
      title: "Plant Water Tracker",
      description:
        "A simple app for keeping track of when your plants need water. Add each plant with how often it " +
        "needs watering, see at a glance which are due, overdue or coming up, and log a watering with one tap.",
      reason: "A clear, self-contained app: build it with sensible defaults.",
      assumptions: [
        "One person uses it; no sign-in.",
        "Each plant has a fixed watering interval in days.",
        "Reminders show inside the app only; no push or email.",
        "Light and dark themes, with a System / Light / Dark setting.",
      ],
    }),
  ],
};

/** Book Club's build: the foundation done, features under way. */
export const booksGoal = {
  title: "Finish the vision",
  level: -1,
  status: "active",
  started_at: ago(35),
  items: [
    { id: "v1", label: "P0.1", title: "App shell, data model and member list", done: true },
    { id: "v2", label: "P0.2", title: "Add a book to read next", done: true },
    { id: "v3", label: "P0.3", title: "Meeting schedule", done: false },
    { id: "v4", label: "P1.1", title: "Ratings and reviews", done: false },
    { id: "v5", label: "P1.2", title: "Reading history", done: false },
  ],
};

const chat = (scope: string, chatId: string, title: string, messages: unknown[], extra: Record<string, unknown> = {}) => ({
  scope,
  chat: chatId,
  title,
  status: "idle",
  agent: "claude",
  model: "claude-opus-5-5",
  notes: "",
  updated_at: ago(1),
  messages,
  actions: [],
  ...extra,
});

/** Book Club while it builds: its app window shows the checklist. */
export const booksChat = chat(
  BOOKS,
  "chat_books",
  "A book club app",
  [message("user", "An app for our book club: what we read next, when we meet, and what we thought", 40)],
  { launch: { stage: "building", started_at: ago(38) }, goal: booksGoal },
);

const looked = message(
  "assistant",
  "**recipe-box** is a small recipe manager: a React front end with Vite, and an Express API that keeps " +
    "recipes in SQLite. Recipes have ingredients, steps and tags, and there is a search page.\n\n" +
    "The stack set up for it matches the code.\n\n" +
    "What likely needs doing, most important first:\n\n" +
    "1. There are no tests yet: add a few for the API and the recipe form.\n" +
    "2. Search only matches titles; it could match ingredients and tags too.\n" +
    "3. The README still describes an old setup.\n" +
    "4. Images are stored as base64 in the database, which will slow it down: worth moving to files?\n\n" +
    "It is a tidy, well-organised codebase, and easy to build on.\n\n" +
    "Each work order stops for your review after its plan and its implementation, and nothing merges until " +
    "you approve it; you can change that in Project settings.\n\n" +
    "I assist here, so you choose what happens: tell me which of these to start, for example \"do 1 and 2\". " +
    "With Build for me, I take the project over instead; the card below switches to it.",
  1,
);

/** An imported project's first look. */
export const recipesChat = chat(RECIPES, "chat_recipes", "First look", [
  message("event", "Mayka is taking a first look at the imported project.", 3),
  looked,
]);

/** Setting up company sign-in with Mayka, on the server. */
export const signInChat = chat(
  PLANTS,
  "chat_signin",
  "Company sign-in",
  [
    message("user", "Help me set up company sign-in for this app. The server's domain is example.com.", 6),
    message(
      "assistant",
      "Happy to. First: does your company already have a sign-in provider, such as Microsoft 365, Google " +
        "Workspace or Okta, or would you like one?",
      5,
    ),
    message("user", "We don't have one. We're about 40 people.", 4),
    message(
      "assistant",
      "For 40 people who would rather not run anything themselves, I'd set up **authentik** on your server. " +
        "It's a full sign-in system: passwords, two-factor, passkeys, and an admin page for your people and " +
        "groups. Commitarium installs and connects it for you.\n\n" +
        "One thing to know first: authentik needs 2 to 3 GB of the server's memory for itself, so a server of " +
        "8 GB or more alongside Commitarium and your apps. Yours has 16 GB, so it fits.\n\n" +
        "To set it up, open **Settings → Server → Company sign-in** and choose **Set up authentik**. It takes a " +
        "few minutes. Then, on this app's Deployment page, choose **Company sign-in** and the groups that may " +
        "open it.",
      3,
    ),
  ],
  { focus: { kind: "sign_in", label: "Company sign-in" } },
);
