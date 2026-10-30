import { Fragment, useCallback, useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import {
  createChat,
  getConversation,
  listChats,
  makeChatProject,
  searchChats,
  type ChatMatch,
  type ChatSummary,
  type Conversation,
} from "../api/assistant";
import { ApiError } from "../api/client";
import { listFeatures } from "../api/features";
import { listProjects } from "../api/projects";
import type { AttentionItem, Feature, Project, RunningWorkOrder } from "../api/types";
import { ChatView, OPEN_ASSISTANT, OPEN_PROJECT, WORK_CHANGED } from "./ChatView";
import {
  ActivityDot,
  chatActivity,
  chatOrders,
  chatWork,
  OrderDotView,
  projectActivity,
  workSignals,
  type ChatWork,
} from "./chatActivity";
import { RECENT_PROJECTS, later, sidebarProjects } from "./sidebarProjects";
import { ProjectCards } from "./ProjectCards";
import { useRail } from "./useRail";
import { Projects } from "./Projects";
import { ProjectPageBody, useProjectPages } from "./ProjectPages";
import type { PageTarget } from "./pageTarget";
import { MarketplaceRelease } from "./MarketplaceRelease";
import { ProjectCost } from "./usage";

const TOP = "commitarium";
const SELECTED_KEY = "commitarium.chats.selected";
const EXPANDED_KEY = "commitarium.chats.expanded";
const ALL_PROJECTS_KEY = "commitarium.chats.allProjects";
const ORDERS_HIDDEN_KEY = "commitarium.chats.ordersHidden";
const SYSTEM_OPEN_KEY = "commitarium.chats.systemOpen";
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

const PINNED_KEY = "commitarium.projects.pinned";
const SIDEBAR_HIDDEN_KEY = "commitarium.shell.sidebarHidden";
const CARDS_HIDDEN_KEY = "commitarium.shell.cardsHidden";
// The window widths below which the rails fold: the sidebar first, as the
// cards hold the project's context while the user works in a chat.
const SIDEBAR_FOLDS_BELOW = 1200;
const CARDS_FOLD_BELOW = 900;
const MIN_SEARCH = 2;
const SEARCH_DELAY_MS = 250;

function readExpanded(key = EXPANDED_KEY): Set<string> {
  try {
    const stored = JSON.parse(localStorage.getItem(key) ?? "[]");
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

/** A project's page open in the centre, over the chat. */
type ShellPage = PageTarget & { scope: string };

// Developer mode's chat shell (ADR-038): chats in a sidebar, grouped under
// their projects, the chat in the centre, and the project's cards on the
// right. A dot on a project or chat pulses while its work runs and turns
// amber when something waits for the user. A project's pages (a work order,
// the vision, settings) open in the centre with a way back to the chat.
export function DevShell({
  reachable,
  attention,
  focusProject,
  onProjectChange,
  openOrder,
  onOrderOpened,
  onForeground,
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
  /** A work order the Inbox asked for; the nonce re-triggers a repeat open. */
  openOrder: { projectId: string; featureId: string; nonce: number } | null;
  /** The asked-for work order is open, so it isn't opened again. */
  onOrderOpened: () => void;
  /** Reports the work order on screen, so its own news isn't also notified. */
  onForeground: (featureId: string | null) => void;
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
  // The open project's work orders, for those listed under its chats.
  const [features, setFeatures] = useState<{ scope: string; list: Feature[] } | null>(null);
  // Chats whose work orders the user folded away, and those showing them all.
  const [ordersHidden, setOrdersHidden] = useState<Set<string>>(() =>
    readExpanded(ORDERS_HIDDEN_KEY),
  );
  const [ordersAll, setOrdersAll] = useState<Set<string>>(() => new Set());
  // The app's own controls fold away under the sidebar's lists.
  const [systemOpen, setSystemOpen] = useState(() => {
    try {
      return localStorage.getItem(SYSTEM_OPEN_KEY) === "true";
    } catch {
      return false;
    }
  });
  const waitingForUser = attention.items.filter((i) => i.actionable).length;
  const read = useRef<Record<string, string>>({});
  const [showProjects, setShowProjects] = useState(false);
  const [allProjects, setAllProjects] = useState(() => {
    try {
      return localStorage.getItem(ALL_PROJECTS_KEY) === "true";
    } catch {
      return false;
    }
  });
  const [searching, setSearching] = useState(false);
  const [query, setQuery] = useState("");
  const sidebar = useRail(SIDEBAR_HIDDEN_KEY, SIDEBAR_FOLDS_BELOW);
  const cards = useRail(CARDS_HIDDEN_KEY, CARDS_FOLD_BELOW);
  // What the search found in the chats' messages; null until it answers.
  const [found, setFound] = useState<ChatMatch[] | null>(null);
  const [reveal, setReveal] = useState<string | undefined>();
  const [pinned, setPinned] = useState<Set<string>>(() => readExpanded(PINNED_KEY));
  const [error, setError] = useState<string | null>(null);
  const [making, setMaking] = useState(false);
  const [page, setPage] = useState<ShellPage | null>(null);
  // A project the assistant just created, until the project list has it.
  const created = useRef<string | null>(null);

  const words = query.trim();
  useEffect(() => {
    setFound(null);
    if (words.length < MIN_SEARCH) return;
    let current = true;
    const timer = setTimeout(() => {
      searchChats(words)
        .then((matches) => current && setFound(matches))
        .catch(() => current && setFound([]));
    }, SEARCH_DELAY_MS);
    return () => {
      current = false;
      clearTimeout(timer);
    };
  }, [words]);

  const select = useCallback((next: Selection) => {
    setSelection(next);
    setShownChat(null);
    setShowProjects(false);
    setPage(null);
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

  // A work order from the Inbox opens in its project, over the chat.
  useEffect(() => {
    if (!openOrder) return;
    const { projectId, featureId } = openOrder;
    expand(projectId);
    setSelection((current) => {
      if (current.scope === projectId) return current;
      const next = { scope: projectId };
      remember(SELECTED_KEY, next);
      setShownChat(null);
      return next;
    });
    setShowProjects(false);
    setPage({ scope: projectId, mode: "order", orderId: featureId });
    onOrderOpened();
  }, [openOrder, onOrderOpened, expand]);
  // A project the assistant created to plan opens on its vision; asking the
  // assistant something goes back to the chat. A page that only opens the
  // assistant beside it stays.
  useEffect(() => {
    const planProject = (event: Event) => {
      const id = (event as CustomEvent<string>).detail;
      if (!id) return;
      created.current = id;
      expand(id);
      select({ scope: id });
      setPage({ scope: id, mode: "vision" });
    };
    const backToChat = (event: Event) => {
      const asked = (event as CustomEvent<{ draft?: string; focus?: unknown; show?: boolean }>)
        .detail;
      if (asked?.draft || asked?.focus || asked?.show) setPage(null);
    };
    window.addEventListener(OPEN_PROJECT, planProject);
    window.addEventListener(OPEN_ASSISTANT, backToChat);
    return () => {
      window.removeEventListener(OPEN_PROJECT, planProject);
      window.removeEventListener(OPEN_ASSISTANT, backToChat);
    };
  }, [expand, select]);

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
  // By value, so a fresh project list with the same projects doesn't load
  // everything again.
  const projectKey = (projects ?? []).map((p) => p.id).join(" ");
  const projectIds = useMemo(() => projectKey.split(" ").filter(Boolean), [projectKey]);
  const loadChats = useCallback(async () => {
    if (!reachable) return;
    // Every project's chats are listed, for the order of the projects; only
    // the shown ones' conversations are read.
    const listed = await Promise.all(
      [...new Set([...scopes, ...projectIds])].map((scope) =>
        listChats(scope)
          .then((list) => [scope, list] as const)
          .catch(() => null),
      ),
    );
    if (selection.scope !== TOP) {
      const scope = selection.scope;
      void listFeatures(scope)
        .then((list) => setFeatures({ scope, list }))
        .catch(() => undefined);
    }
    const found = Object.fromEntries(listed.filter((entry) => entry !== null));
    setChats((current) => ({ ...current, ...found }));
    // A chat's cards are read again only once it has changed.
    const changed = Object.values(found)
      .flat()
      .filter((chat) => scopes.includes(chat.scope))
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
  }, [reachable, scopes, projectIds, selection.scope]);
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

  // A project that is gone leaves its chats for the loose ones; one just
  // created waits for the list to catch up.
  useEffect(() => {
    if (!projects || selection.scope === TOP) return;
    if (projects.some((p) => p.id === selection.scope)) {
      if (created.current === selection.scope) created.current = null;
    } else if (created.current !== selection.scope) {
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
  // A page belongs to the project it was opened in.
  const shownPage = page && page.scope === selection.scope && !loose ? page : null;
  const scope = selection.scope;
  const setScopePage = useCallback(
    (next: PageTarget | null) => setPage(next ? { ...next, scope } : null),
    [scope],
  );
  const pageOpen = !!shownPage && !showProjects;
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
  const chatButton = (chat: ChatSummary, sub: boolean, all = false, toggle?: ReactNode) =>
    (all || match(chat.title || "New chat")) && (
      <button
        key={chat.id}
        className={`chat-side__item ${sub ? "chat-side__item--sub" : ""} ${
          selection.scope === chat.scope && currentChat === chat.id ? "chat-side__item--on" : ""
        }`}
        onClick={() => select({ scope: chat.scope, chat: chat.id })}
        title={chat.title || "New chat"}
      >
        <span className={`chat-side__name ${toggle ? "chat-side__name--fit" : ""}`}>
          {chat.title || "New chat"}
        </span>
        {toggle}
        <ActivityDot activity={activityOf(chat)} />
      </button>
    );
  // The work orders the open chat started, under it: every one still going
  // and the latest finished, four in all, unless the user shows them all or
  // folds them away.
  const flip = (set: Set<string>, key: string) => {
    const next = new Set(set);
    if (!next.delete(key)) next.add(key);
    return next;
  };
  const chatWithOrders = (chat: ChatSummary, all: boolean) => {
    const key = `${chat.scope}/${chat.id}`;
    const shown =
      chat.scope === selection.scope && chat.id === currentChat && features?.scope === chat.scope;
    const started = (chatWorks[key]?.started ?? []).filter(
      (id) => features?.scope !== chat.scope || features.list.some((f) => f.id === id),
    );
    if (started.length === 0) return chatButton(chat, true, all);
    const hidden = !shown || ordersHidden.has(key);
    const unfold = () => {
      setOrdersHidden((current) => {
        if (!current.has(key)) return current;
        const next = flip(current, key);
        remember(ORDERS_HIDDEN_KEY, [...next]);
        return next;
      });
      if (!shown) select({ scope: chat.scope, chat: chat.id });
    };
    // Folded, or under a chat not on screen, its work orders are one line, as
    // Claude Code shows a session's nested sessions: how many, and what of
    // them is going on.
    const working = started.filter((id) => signals.running.has(id)).length;
    const waiting = started.filter((id) => signals.waiting.has(id)).length;
    const nested = (
      <button
        key={`${chat.id}:nested`}
        className="chat-side__item chat-side__item--nested"
        onClick={unfold}
        title="Show its work orders"
      >
        <span className="chat-side__elbow" aria-hidden />
        <span className="chat-side__name">
          {started.length} work order{started.length === 1 ? "" : "s"}
          {waiting > 0 && (
            <span className="chat-side__nested-waiting"> · {waiting} waits for you</span>
          )}
          {working > 0 && <span className="chat-side__nested-working"> · {working} working</span>}
        </span>
      </button>
    );
    if (!shown) {
      return (
        <Fragment key={chat.id}>
          {chatButton(chat, true, all)}
          {nested}
        </Fragment>
      );
    }
    const { orders, more } = chatOrders(
      started,
      features?.list ?? [],
      signals,
      ordersAll.has(key) ? Infinity : undefined,
    );
    const on = (id: string) => shownPage?.mode === "order" && shownPage.orderId === id;
    const fold = () =>
      setOrdersHidden((current) => {
        const next = flip(current, key);
        remember(ORDERS_HIDDEN_KEY, [...next]);
        return next;
      });
    // Right of the chat's name, so its work orders read as its own and not
    // as more chats. It sits inside the chat's button, so it acts on its own.
    const toggle = (
      <span
        role="button"
        tabIndex={0}
        className="chat-side__fold"
        onClick={(e) => {
          e.stopPropagation();
          fold();
        }}
        onKeyDown={(e) => {
          if (e.key !== "Enter" && e.key !== " ") return;
          e.preventDefault();
          e.stopPropagation();
          fold();
        }}
        aria-expanded={!hidden}
        aria-label={hidden ? "Show its work orders" : "Hide its work orders"}
        title={hidden ? "Show its work orders" : "Hide its work orders"}
      >
        <Chevron open={!hidden} />
      </span>
    );
    return (
      <Fragment key={chat.id}>
        {chatButton(chat, true, all, toggle)}
        {hidden && nested}
        {!hidden &&
          orders.map(({ feature, dot }) => (
            <button
              key={feature.id}
              className={`chat-side__item chat-side__item--order ${on(feature.id) ? "chat-side__item--on" : ""}`}
              onClick={() => setScopePage({ mode: "order", orderId: feature.id })}
              title={feature.title}
            >
              <OrderDotView dot={dot} />
              <span className="chat-side__name">{feature.title}</span>
            </button>
          ))}
        {!hidden && (more > 0 || ordersAll.has(key)) && (
          <button
            className="chat-side__item chat-side__item--order chat-side__item--quiet"
            onClick={() => setOrdersAll((current) => flip(current, key))}
          >
            {ordersAll.has(key) ? "Show fewer" : `Show ${more} more`}
          </button>
        )}
      </Fragment>
    );
  };
  const activities = Object.fromEntries(
    (projects ?? []).map((p) => [
      p.id,
      projectActivity(
        p.id,
        attention.items,
        attention.running,
        (chats[p.id] ?? []).map(activityOf),
      ),
    ]),
  );
  // The most recently worked on (a chat of it changed) come first, so
  // opening one doesn't move it; the rest fold under "Show all",
  // except any that is busy or on screen. A search looks through them all.
  const recent = Object.fromEntries(
    (projects ?? []).map((p) => [
      p.id,
      (chats[p.id] ?? []).reduce<string | undefined>(
        (at, c) => later(at, c.updated_at),
        undefined,
      ) ?? p.created_at,
    ]),
  );
  const keep = new Set(
    (projects ?? [])
      .filter((p) => p.id === selection.scope || activities[p.id] !== "idle")
      .map((p) => p.id),
  );
  const listed = sidebarProjects(
    projects ?? [],
    recent,
    keep,
    query.trim() || allProjects ? Infinity : RECENT_PROJECTS,
    pinned,
  );
  const folded = sidebarProjects(projects ?? [], recent, keep, RECENT_PROJECTS, pinned).hidden;
  const togglePin = (id: string) =>
    setPinned((current) => {
      const next = flip(current, id);
      remember(PINNED_KEY, [...next]);
      return next;
    });
  const nameOf = (scope: string) =>
    scope === TOP ? "Chats" : projects?.find((p) => p.id === scope)?.name;
  const openMatch = (match: ChatMatch) => {
    if (match.scope !== TOP) expand(match.scope);
    select({ scope: match.scope, chat: match.chat });
    setReveal(match.message_id);
  };
  const visibleProjects = listed.shown.filter(
    (p) => match(p.name) || (chats[p.id] ?? []).some((c) => match(c.title || "New chat")),
  );
  const showAll = (on: boolean) => {
    setAllProjects(on);
    remember(ALL_PROJECTS_KEY, on);
  };

  return (
    <div className="dev-shell">
      <nav className={`chat-side ${sidebar.hidden ? "chat-side--folded" : ""}`} aria-label="Chats">
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

        {/* Projects and chats scroll; New chat above and the app's own
            controls below stay put. */}
        <div className="chat-side__scroll">
          {words.length >= MIN_SEARCH && (
            <>
              <div className="chat-side__label">In messages</div>
              {found === null && <p className="muted chat-side__note">Searching…</p>}
              {found?.length === 0 && <p className="muted chat-side__note">Nothing found.</p>}
              {found?.map((match) => (
                <button
                  key={`${match.scope}/${match.chat}`}
                  className="chat-side__item chat-side__match"
                  onClick={() => openMatch(match)}
                  title={match.title || "New chat"}
                >
                  <span className="chat-side__match-title">
                    {match.title || "New chat"}
                    {nameOf(match.scope) && <span className="muted"> · {nameOf(match.scope)}</span>}
                  </span>
                  {match.snippet && (
                    <span className="muted chat-side__match-text">{match.snippet}</span>
                  )}
                </button>
              ))}
            </>
          )}
          <div className="chat-side__label">
            Projects
            <button
              className="ghost chat-side__all"
              onClick={() => {
                setPage(null);
                setShowProjects(true);
              }}
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
                  <span
                    role="button"
                    tabIndex={0}
                    className={`chat-side__pin ${pinned.has(p.id) ? "chat-side__pin--on" : ""}`}
                    onClick={(e) => {
                      e.stopPropagation();
                      togglePin(p.id);
                    }}
                    onKeyDown={(e) => {
                      if (e.key !== "Enter" && e.key !== " ") return;
                      e.preventDefault();
                      e.stopPropagation();
                      togglePin(p.id);
                    }}
                    aria-pressed={pinned.has(p.id)}
                    aria-label={pinned.has(p.id) ? `Unpin ${p.name}` : `Pin ${p.name}`}
                    title={pinned.has(p.id) ? "Unpin" : "Pin to the top"}
                  >
                    <PinIcon />
                  </span>
                  <ActivityDot activity={activities[p.id]} />
                </button>
                {open && (
                  <>
                    {(chats[p.id] ?? []).map((chat) => chatWithOrders(chat, match(p.name)))}
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
          {folded > 0 && !query.trim() && (
            <button
              className="chat-side__item chat-side__item--quiet"
              onClick={() => showAll(!allProjects)}
            >
              {allProjects ? "Show fewer" : `Show all (${folded} more)`}
            </button>
          )}

          <div className="chat-side__label">Chats</div>
          {(chats[TOP] ?? []).map((chat) => chatButton(chat, false))}
        </div>

        <div className="chat-side__foot">
          <button
            className="chat-side__item chat-side__item--quiet chat-side__foot-toggle"
            onClick={() => {
              setSystemOpen(!systemOpen);
              remember(SYSTEM_OPEN_KEY, !systemOpen);
            }}
            aria-expanded={systemOpen}
          >
            <span className="chat-side__name">Inbox and settings</span>
            {!systemOpen && waitingForUser > 0 && (
              <span className="topbar__badge">{waitingForUser}</span>
            )}
            <Chevron open={systemOpen} />
          </button>
          {systemOpen && systemNav}
        </div>
      </nav>

      <main className="dev-shell__main">
        <RailHandle side="left" name="sidebar" hidden={sidebar.hidden} onToggle={sidebar.toggle} />
        <RailHandle
          side="right"
          name="project cards"
          hidden={cards.hidden}
          onToggle={cards.toggle}
        />
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
        {!loose && (
          <ProjectCentre
            key={selection.scope}
            projectId={selection.scope}
            page={showProjects ? null : shownPage}
            setPage={setScopePage}
            attention={attention.items}
            onForeground={onForeground}
            onRenamed={loadProjects}
          />
        )}
        {/* The chat stays mounted under the project list and the project's
            pages, so a project created there can open its chat on a started
            message, and coming back finds the chat as it was. */}
        <div className="dev-shell__chat" hidden={showProjects || pageOpen}>
          <ChatView
            scope={selection.scope}
            chat={selection.chat}
            title={chatTitle || "New chat"}
            work={signals}
            onConversation={onConversation}
            reveal={reveal}
            onRevealed={() => setReveal(undefined)}
            onMoved={(scope, chat) => {
              expand(scope);
              select({ scope, chat });
              loadProjects();
              void loadChats();
            }}
            onOpenChat={(chat) => select({ scope: selection.scope, chat })}
            planSwitch={false}
            projectName={loose ? undefined : project?.name}
            chatName={(chat) => {
              const found = chats[selection.scope]?.find((c) => c.id === chat);
              return found ? found.title || "New chat" : undefined;
            }}
            onOpenOrder={loose ? undefined : (id) => setScopePage({ mode: "order", orderId: id })}
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
                {loose && (
                  <button
                    className="primary"
                    onClick={() => void makeProject()}
                    disabled={making || !currentChat}
                  >
                    {making ? "Making it a project…" : "Make it a project"}
                  </button>
                )}
              </>
            }
          />
        </div>
      </main>

      <aside
        className={`dev-shell__cards ${cards.hidden ? "dev-shell__cards--folded" : ""}`}
        aria-label={loose ? "No project yet" : "Project"}
      >
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
            chat={currentChat}
            attention={attention}
            onOpenPage={(target) => {
              setShowProjects(false);
              setScopePage(target);
            }}
          />
        )}
      </aside>
    </div>
  );
}

// The selected project's pages in the centre. It stays mounted while the
// project is selected, page open or not, so its preview setup is followed
// as on the project page.
function ProjectCentre({
  projectId,
  page,
  setPage,
  attention,
  onForeground,
  onRenamed,
}: {
  projectId: string;
  /** The page on screen, or null while the chat shows. */
  page: PageTarget | null;
  setPage: (page: PageTarget | null) => void;
  attention: AttentionItem[];
  onForeground: (featureId: string | null) => void;
  /** The project's settings renamed it. */
  onRenamed: () => void;
}) {
  const pages = useProjectPages(projectId, { page, setPage });
  const items = useMemo(
    () => attention.filter((i) => i.project_id === projectId),
    [attention, projectId],
  );
  const { mode, orderId, project } = pages;
  useEffect(() => {
    onForeground(mode === "order" ? orderId : null);
  }, [mode, orderId, onForeground]);
  useEffect(() => () => onForeground(null), [onForeground]);
  if (!mode) return null;

  const fill = mode === "order" || mode === "graph";
  return (
    <div className={`dev-shell__page ${fill ? "dev-shell__page--fill" : ""}`}>
      <button className="ghost dev-shell__back" onClick={() => setPage(null)}>
        ← Back to chat
      </button>
      <div className={`main__inner ${fill ? "main__inner--fill" : ""}`}>
        {pages.error && <div className="banner banner--error">{pages.error}</div>}
        {/* What the project page's overview showed besides its work. */}
        {mode === "settings" && project && (
          <div className="dev-shell__extras">
            {project.app && !pages.needsStack && <MarketplaceRelease projectId={projectId} />}
            <ProjectCost projectId={projectId} onOpenOrder={pages.openOrder} />
          </div>
        )}
        <ProjectPageBody pages={pages} attention={items} onLoaded={onRenamed} />
      </div>
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

/** Folds a rail away: a small handle on its edge, there open or closed. */
function RailHandle({
  side,
  name,
  hidden,
  onToggle,
}: {
  side: "left" | "right";
  name: string;
  hidden: boolean;
  onToggle: () => void;
}) {
  const label = `${hidden ? "Show" : "Hide"} the ${name}`;
  // The chevron points the way the rail will move.
  const towardsLeft = side === "left" ? !hidden : hidden;
  return (
    <button
      className={`rail-handle rail-handle--${side}`}
      onClick={onToggle}
      aria-expanded={!hidden}
      aria-label={label}
      title={label}
    >
      <svg width="8" height="12" viewBox="0 0 8 12" aria-hidden>
        <path
          d={towardsLeft ? "M6 2 2 6l4 4" : "M2 2l4 4-4 4"}
          fill="none"
          stroke="currentColor"
          strokeWidth="1.4"
          strokeLinecap="round"
          strokeLinejoin="round"
        />
      </svg>
    </button>
  );
}

function PinIcon() {
  return (
    <svg width="12" height="12" viewBox="0 0 16 16" aria-hidden>
      <path
        d="M6 2h4v5l2 2v1H4V9l2-2V2Zm2 8v4"
        fill="none"
        stroke="currentColor"
        strokeWidth="1.4"
        strokeLinecap="round"
        strokeLinejoin="round"
      />
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
