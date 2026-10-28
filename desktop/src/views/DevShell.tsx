import { useCallback, useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import {
  createChat,
  getConversation,
  listChats,
  makeChatProject,
  type ChatSummary,
  type Conversation,
} from "../api/assistant";
import { ApiError } from "../api/client";
import { listProjects } from "../api/projects";
import type { AttentionItem, Project, RunningWorkOrder } from "../api/types";
import { ChatView, OPEN_PROJECT, WORK_CHANGED } from "./ChatView";
import {
  ActivityDot,
  chatActivity,
  chatWork,
  projectActivity,
  workSignals,
  type ChatWork,
} from "./chatActivity";
import { ProjectCards } from "./ProjectCards";
import { Projects } from "./Projects";

const TOP = "commitarium";
const SELECTED_KEY = "commitarium.chats.selected";
const EXPANDED_KEY = "commitarium.chats.expanded";
const POLL_MS = 10_000;

/** The chat on screen: a scope's chat, or its most recently used one. */
interface Selection {
  scope: string;
  chat?: string;
}

function readSelection(): Selection {
  try {
    const stored = JSON.parse(localStorage.getItem(SELECTED_KEY) ?? "null");
    if (stored && typeof stored.scope === "string") {
      return {
        scope: stored.scope,
        chat: typeof stored.chat === "string" ? stored.chat : undefined,
      };
    }
  } catch {
    // A first run, or storage that cannot be read: the loose chats.
  }
  return { scope: TOP };
}

function readExpanded(): Set<string> {
  try {
    const stored = JSON.parse(localStorage.getItem(EXPANDED_KEY) ?? "[]");
    if (Array.isArray(stored)) return new Set(stored.filter((id) => typeof id === "string"));
  } catch {
    // Nothing remembered yet.
  }
  return new Set();
}

function remember(key: string, value: unknown) {
  try {
    localStorage.setItem(key, JSON.stringify(value));
  } catch {
    // It lasts until the window closes.
  }
}

// Developer mode's chat shell (ADR-038): chats in a sidebar, grouped under
// their projects, the chat in the centre, and the project's cards on the
// right. A dot on a project or chat pulses while its work runs and turns
// amber when something waits for the user.
export function DevShell({
  reachable,
  attention,
  focusProject,
  onProjectChange,
  onOpenOrder,
  onOpenProjectPage,
  areaNav,
  systemNav,
}: {
  reachable: boolean;
  /** The attention snapshot the Inbox shows. */
  attention: { items: AttentionItem[]; running: RunningWorkOrder[] };
  /** A project to show, as one the assistant just created. */
  focusProject: string | null;
  /** The project of the chat on screen, or null for a loose chat. */
  onProjectChange: (projectId: string | null) => void;
  onOpenOrder: (projectId: string, featureId: string) => void;
  /** The project's own page, until its parts move here (D6). */
  onOpenProjectPage: (projectId: string) => void;
  /** The way to the Apps and Marketplace areas. */
  areaNav: ReactNode;
  /** The app's own controls: Inbox, Settings, the coordinator's status. */
  systemNav: ReactNode;
}) {
  const [selection, setSelection] = useState<Selection>(() =>
    focusProject ? { scope: focusProject } : readSelection(),
  );
  const [expanded, setExpanded] = useState<Set<string>>(readExpanded);
  // The chat the scope's own routes answered, for a selection without one.
  const [shownChat, setShownChat] = useState<string | null>(null);
  const [projects, setProjects] = useState<Project[] | null>(null);
  const [chats, setChats] = useState<Record<string, ChatSummary[]>>({});
  // What each chat started and asks, read again when it changes.
  const [chatWorks, setChatWorks] = useState<Record<string, ChatWork>>({});
  const read = useRef<Record<string, string>>({});
  const [showProjects, setShowProjects] = useState(false);
  const [searching, setSearching] = useState(false);
  const [query, setQuery] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [making, setMaking] = useState(false);

  const select = useCallback((next: Selection) => {
    setSelection(next);
    setShownChat(null);
    setShowProjects(false);
    setError(null);
    remember(SELECTED_KEY, next);
  }, []);
  const expand = useCallback((id: string, open = true) => {
    setExpanded((current) => {
      if (current.has(id) === open) return current;
      const next = new Set(current);
      if (open) next.add(id);
      else next.delete(id);
      remember(EXPANDED_KEY, [...next]);
      return next;
    });
  }, []);

  // A project opened from elsewhere shows its most recently used chat.
  useEffect(() => {
    if (!focusProject) return;
    setSelection((current) => {
      if (current.scope === focusProject) return current;
      const next = { scope: focusProject };
      remember(SELECTED_KEY, next);
      setShownChat(null);
      setShowProjects(false);
      return next;
    });
    expand(focusProject);
  }, [focusProject, expand]);
  useEffect(() => {
    onProjectChange(selection.scope === TOP ? null : selection.scope);
  }, [selection.scope, onProjectChange]);

  // Projects, and the chats of the loose list, the open projects, and the
  // one on screen.
  const scopes = useMemo(
    () => [...new Set([TOP, selection.scope, ...expanded])],
    [selection.scope, expanded],
  );
  const loadProjects = useCallback(() => {
    if (!reachable) return;
    listProjects()
      .then(setProjects)
      .catch(() => undefined);
  }, [reachable]);
  const loadChats = useCallback(async () => {
    if (!reachable) return;
    const listed = await Promise.all(
      scopes.map((scope) =>
        listChats(scope)
          .then((list) => [scope, list] as const)
          .catch(() => null),
      ),
    );
    const found = Object.fromEntries(listed.filter((entry) => entry !== null));
    setChats((current) => ({ ...current, ...found }));
    // A chat's cards are read again only once it has changed.
    const changed = Object.values(found)
      .flat()
      .filter((chat) => read.current[`${chat.scope}/${chat.id}`] !== chat.updated_at);
    await Promise.all(
      changed.map((chat) =>
        getConversation({ scope: chat.scope, chat: chat.id })
          .then((conversation) => {
            const key = `${chat.scope}/${chat.id}`;
            read.current[key] = chat.updated_at;
            setChatWorks((current) => ({ ...current, [key]: chatWork(conversation) }));
          })
          .catch(() => undefined),
      ),
    );
  }, [reachable, scopes]);
  useEffect(() => {
    loadProjects();
    void loadChats();
    const timer = setInterval(() => void loadChats(), POLL_MS);
    return () => clearInterval(timer);
  }, [loadProjects, loadChats]);
  useEffect(() => {
    const reload = () => {
      loadProjects();
      void loadChats();
    };
    window.addEventListener(WORK_CHANGED, reload);
    window.addEventListener(OPEN_PROJECT, reload);
    return () => {
      window.removeEventListener(WORK_CHANGED, reload);
      window.removeEventListener(OPEN_PROJECT, reload);
    };
  }, [loadProjects, loadChats]);

  // A project that is gone leaves its chats for the loose ones.
  useEffect(() => {
    if (projects && selection.scope !== TOP && !projects.some((p) => p.id === selection.scope)) {
      select({ scope: TOP });
    }
  }, [projects, selection.scope, select]);

  const signals = useMemo(
    () => workSignals(attention.items, attention.running),
    [attention.items, attention.running],
  );
  const activityOf = (chat: ChatSummary) =>
    chatActivity(chat, chatWorks[`${chat.scope}/${chat.id}`], signals);

  const onConversation = useCallback(
    (conversation: Conversation | null) => {
      if (conversation?.chat && conversation.scope === selection.scope) {
        setShownChat(conversation.chat);
      }
    },
    [selection.scope],
  );
  const currentChat = selection.chat ?? shownChat ?? undefined;
  const loose = selection.scope === TOP;
  const project = projects?.find((p) => p.id === selection.scope);
  const chatTitle = chats[selection.scope]?.find((c) => c.id === currentChat)?.title;

  const newChat = async (scope: string) => {
    setError(null);
    try {
      const created = await createChat(scope);
      if (scope !== TOP) expand(scope);
      select({ scope, chat: created.chat });
      void loadChats();
    } catch (e) {
      setError(describe(e));
    }
  };

  // The chat moves into a new project as its first chat (D2's route).
  const makeProject = async () => {
    if (!currentChat) return;
    setMaking(true);
    setError(null);
    try {
      const moved = await makeChatProject(currentChat);
      expand(moved.scope);
      select({ scope: moved.scope, chat: moved.chat });
      loadProjects();
      void loadChats();
    } catch (e) {
      setError(
        // A coordinator without the route answers a bare 404.
        e instanceof ApiError && e.status === 404 && e.code === "unknown_error"
          ? "Making a chat a project needs a newer coordinator. Update Commitarium and try again."
          : describe(e),
      );
    } finally {
      setMaking(false);
    }
  };

  const match = (text: string) => !query.trim() || text.toLowerCase().includes(query.toLowerCase());
  // A project found by its name shows all its chats.
  const chatButton = (chat: ChatSummary, sub: boolean, all = false) =>
    (all || match(chat.title || "New chat")) && (
      <button
        key={chat.id}
        className={`chat-side__item ${sub ? "chat-side__item--sub" : ""} ${
          selection.scope === chat.scope && currentChat === chat.id ? "chat-side__item--on" : ""
        }`}
        onClick={() => select({ scope: chat.scope, chat: chat.id })}
        title={chat.title || "New chat"}
      >
        <span className="chat-side__name">{chat.title || "New chat"}</span>
        <ActivityDot activity={activityOf(chat)} />
      </button>
    );
  const visibleProjects = (projects ?? []).filter(
    (p) => match(p.name) || (chats[p.id] ?? []).some((c) => match(c.title || "New chat")),
  );

  return (
    <div className="dev-shell">
      <nav className="chat-side" aria-label="Chats">
        <div className="chat-side__brand">
          <span>Commitarium</span>
          <span className="pill chat-side__mode">Developer</span>
        </div>
        <div className="chat-side__areas">{areaNav}</div>
        <button className="chat-side__item" onClick={() => void newChat(TOP)}>
          <PlusIcon />
          New chat
        </button>
        {searching ? (
          <input
            className="chat-side__search"
            autoFocus
            placeholder="Search chats"
            aria-label="Search chats"
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === "Escape") {
                setQuery("");
                setSearching(false);
              }
            }}
            onBlur={() => !query && setSearching(false)}
          />
        ) : (
          <button className="chat-side__item" onClick={() => setSearching(true)}>
            <SearchIcon />
            Search
          </button>
        )}

        <div className="chat-side__label">
          Projects
          <button
            className="ghost chat-side__all"
            onClick={() => setShowProjects(true)}
            title="All projects, with New project and Import"
          >
            All
          </button>
        </div>
        {projects === null && <p className="muted chat-side__note">Loading…</p>}
        {projects?.length === 0 && (
          <p className="muted chat-side__note">None yet. Start a chat and make it a project.</p>
        )}
        {visibleProjects.map((p) => {
          const open = expanded.has(p.id) || !!query.trim();
          return (
            <div key={p.id} className="chat-side__group">
              <button
                className={`chat-side__item ${
                  selection.scope === p.id && !open ? "chat-side__item--on" : ""
                }`}
                onClick={() => expand(p.id, !expanded.has(p.id))}
                aria-expanded={open}
                title={p.name}
              >
                <Chevron open={open} />
                <span className="chat-side__name">{p.name}</span>
                <ActivityDot
                  activity={projectActivity(
                    p.id,
                    attention.items,
                    attention.running,
                    (chats[p.id] ?? []).map(activityOf),
                  )}
                />
              </button>
              {open && (
                <>
                  {(chats[p.id] ?? []).map((chat) => chatButton(chat, true, match(p.name)))}
                  <button
                    className="chat-side__item chat-side__item--sub chat-side__item--quiet"
                    onClick={() => void newChat(p.id)}
                  >
                    + New chat here
                  </button>
                </>
              )}
            </div>
          );
        })}

        <div className="chat-side__label">Chats</div>
        {(chats[TOP] ?? []).map((chat) => chatButton(chat, false))}

        <div className="chat-side__foot">{systemNav}</div>
      </nav>

      <main className="dev-shell__main">
        {showProjects && (
          <div className="dev-shell__page">
            <button className="ghost" onClick={() => setShowProjects(false)}>
              ← Back to chat
            </button>
            <Projects
              reachable={reachable}
              onSelect={(id) => {
                expand(id);
                select({ scope: id });
              }}
            />
          </div>
        )}
        {/* The chat stays mounted under the project list, so a project
            created there can open its chat on a started message. */}
        <div className="dev-shell__chat" hidden={showProjects}>
          <ChatView
            scope={selection.scope}
            chat={selection.chat}
            title={chatTitle || "New chat"}
            work={signals}
            onConversation={onConversation}
            onMoved={(scope, chat) => {
              expand(scope);
              select({ scope, chat });
              loadProjects();
              void loadChats();
            }}
            onOpenOrder={loose ? undefined : (id) => onOpenOrder(selection.scope, id)}
            chips={
              loose ? (
                <span className="pill chat-view__chip">No project</span>
              ) : (
                <span className="pill chat-view__chip">{project?.name ?? "…"}</span>
              )
            }
            actions={
              <>
                {error && <span className="chat-view__error">{error}</span>}
                {loose ? (
                  <button
                    className="primary"
                    onClick={() => void makeProject()}
                    disabled={making || !currentChat}
                  >
                    {making ? "Making it a project…" : "Make it a project"}
                  </button>
                ) : (
                  <button
                    className="ghost"
                    onClick={() => onOpenProjectPage(selection.scope)}
                    title="The project's page, with its work orders, preview, and settings"
                  >
                    Project page
                  </button>
                )}
              </>
            }
          />
        </div>
      </main>

      <aside className="dev-shell__cards" aria-label={loose ? "No project yet" : "Project"}>
        {loose ? (
          <section className="shell-card">
            <h2 className="shell-card__head">No project yet</h2>
            <p className="muted shell-card__text">
              A chat is free talk. When it turns into something to build, make it a project: this
              chat becomes its first conversation, and the project&apos;s vision, work orders,
              agents, preview and sync show here.
            </p>
            <button
              className="primary"
              onClick={() => void makeProject()}
              disabled={making || !currentChat}
            >
              Make it a project
            </button>
          </section>
        ) : (
          <ProjectCards
            key={selection.scope}
            projectId={selection.scope}
            project={project}
            attention={attention}
            onOpenOrder={onOpenOrder}
            onOpenProjectPage={onOpenProjectPage}
          />
        )}
      </aside>
    </div>
  );
}

function describe(e: unknown): string {
  return e instanceof ApiError ? e.message : String(e);
}

function PlusIcon() {
  return (
    <svg width="14" height="14" viewBox="0 0 16 16" aria-hidden>
      <path d="M8 3v10M3 8h10" stroke="currentColor" strokeWidth="1.6" strokeLinecap="round" />
    </svg>
  );
}

function SearchIcon() {
  return (
    <svg width="14" height="14" viewBox="0 0 16 16" aria-hidden>
      <circle cx="7" cy="7" r="4.5" fill="none" stroke="currentColor" strokeWidth="1.5" />
      <path d="m10.5 10.5 3 3" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" />
    </svg>
  );
}

function Chevron({ open }: { open: boolean }) {
  return (
    <svg
      width="10"
      height="10"
      viewBox="0 0 10 10"
      aria-hidden
      className={`chat-side__chevron ${open ? "chat-side__chevron--open" : ""}`}
    >
      <path
        d="M3 2l3 3-3 3"
        fill="none"
        stroke="currentColor"
        strokeWidth="1.4"
        strokeLinecap="round"
        strokeLinejoin="round"
      />
    </svg>
  );
}
