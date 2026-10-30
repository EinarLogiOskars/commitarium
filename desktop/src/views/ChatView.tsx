import { SCREENSHOT_PROMPT, captureScreenshot, nextPaint } from "./screenshot";
import { AttachmentChips, SentAttachments, useAttachments } from "./Attachments";
import { ComposerBox } from "./Composer";
import {
  useCallback,
  useEffect,
  useMemo,
  useRef,
  useState,
  type ReactElement,
  type ReactNode,
} from "react";
import {
  approveAssistantAction,
  dismissAssistantAction,
  getAssistantSettings,
  type AssistantSettings,
  getConversation,
  retryAssistantTurn,
  saveAssistantNotes,
  sendToAssistant,
  setAssistantFocus,
  type AssistantFocus,
  type AssistantAction,
  type AutopilotGoal,
  type ChatTarget,
  type LaunchStage,
  type Conversation,
} from "../api/assistant";
import { ApiError } from "../api/client";
import { openAppWindow, webviewSnapshot, webviewSnapshotSupported } from "../ipc";
import { getVision } from "../api/vision";
import { developerModeOn } from "../devMode";
import { getProject } from "../api/projects";
import { listFeatures } from "../api/features";
import type {
  AssistantMode,
  Feature,
  Vision,
  VisionItem,
  VisionPlanning,
  VisionPriority,
} from "../api/types";
import {
  appliesItself,
  assistantMode,
  assistantModeNote,
  autoAppliedInBuilds,
} from "./assistantMode";
import { Markdown } from "./Markdown";
import { useCatalogModels, useModelName } from "./useModelName";
import { agentName, reloadAgents, useAgentList, useAgents } from "./useAgents";
import type { AgentRole } from "../api/types";
import { Agents } from "./Agents";
import { AgentLogin } from "./AgentLogin";
import { assistantGuidance, GUIDANCE } from "./setupGuidance";
import { appliedWhat, chooseAgent, choiceOverrides, type RoleChoice } from "./actionCard";
import { AssistantActivityLine, TurnFailureControls } from "./AssistantActivity";
import { AppliedGroup } from "./AppliedGroup";
import { foldedEvent, groupTurn } from "./appliedSummary";
import {
  appliedLine,
  dropRepeats,
  eventLine,
  foldQuiet,
  groupLine,
  orderLine,
  type QuietEntry,
  type QuietLine,
} from "./quietLines";
import { QuietRow, QuietRun, QuietText } from "./QuietRow";
import { ASSISTANT_CHANGED, OPEN_PLAN, WORK_CHANGED, WRITE_PLAN } from "./chatEvents";
import {
  chatPlanning,
  planChanges,
  planChangeShows,
  passedOverQuestions,
  planCommand,
  planMenuLabel,
  planPhase,
  WRITE_THE_PLAN,
  someOf,
  type PlanChange,
} from "./planMode";
import { usePlanSwitch } from "./usePlanSwitch";
import { StopPlanDialog } from "./StopPlanDialog";
import { QuestionCards } from "./QuestionCards";
import { isLiveOrder, ORDER_STATE_LABEL, type WorkSignals } from "./chatActivity";

const IDLE_POLL_MS = 20_000;
const POLL_MS = 2000;

export { ASSISTANT_CHANGED, WORK_CHANGED };
/** Asks the open project to show its Vision page. */
export const OPEN_VISION = "commitarium:open-vision";
/** Opens the assistant, optionally with a message started in its composer. */
export const OPEN_ASSISTANT = "commitarium:open-assistant";
/** Opens a project (detail: its ID), as one the assistant created to plan. */
export const OPEN_PROJECT = "commitarium:open-project";

const STEPS_LABEL: Record<string, string> = {
  full: "Full (plan, review)",
  planned: "Planned (no reviewer)",
  quick: "Quick change",
};

// A conversation with the assistant (ASSISTANT_PLAN.md): its messages, event
// lines, cards, live activity, and the composer. The window's drawer shows a
// scope's most recently used chat in it; Developer mode's chat shell
// (ADR-038) shows the chat the user picked as its main view.
export function ChatView({
  scope,
  chat,
  title,
  variant = "page",
  open = true,
  onOpenChange,
  docked = false,
  onOpenOrder,
  onGoalChange,
  onLaunchChange,
  onConversation,
  onMoved,
  onOpenChat,
  chatName,
  planSwitch = true,
  projectName,
  onCapturingChange,
  screenshot = false,
  chips,
  actions,
  work,
}: {
  scope: string;
  /** The chat to show; without one, the scope's most recently used chat. */
  chat?: string;
  /** The drawer's title; a page shows the chat's own title. */
  title: string;
  /** In the window's drawer, or the main view of the chat shell. */
  variant?: "drawer" | "page";
  /** The drawer is open; it keeps loading while closed. */
  open?: boolean;
  onOpenChange?: (open: boolean) => void;
  /** Beside the page rather than over it. */
  docked?: boolean;
  onOpenOrder?: (featureId: string) => void;
  /** The conversation's autopilot goal while it is on, else null. */
  onGoalChange?: (goal: AutopilotGoal | null) => void;
  /** A new app's launch stage while it is planned or set up, else null. */
  onLaunchChange?: (stage: LaunchStage | null) => void;
  /** Each conversation as it loads. */
  onConversation?: (conversation: Conversation | null) => void;
  /** The chat moved into the app or project an approved card created
   * (ADR-038): the shell follows it there. */
  onMoved?: (scope: string, chat: string) => void;
  /** Opens another chat of the scope, as the one the project is planned in. */
  onOpenChat?: (chat: string) => void;
  /** Another chat's title, by its ID. */
  chatName?: (chat: string) => string | undefined;
  /** The header carries the plan switch; the chat shell has it on its
   * cards instead. */
  planSwitch?: boolean;
  /** The project's name, which starts its vision when plan mode is turned
   * on without one. */
  projectName?: string;
  /** While a screenshot is taken, so the drawer's button hides too. */
  onCapturingChange?: (capturing: boolean) => void;
  /** Offers "Take a screenshot and describe what to change": over an app's
   * own window, a capture of the app as the user sees it. */
  screenshot?: boolean;
  /** A page's header: what goes before the mode, and the buttons at its end. */
  chips?: ReactNode;
  actions?: ReactNode;
  /** The work running and waiting on the user, for live work order cards. */
  work?: WorkSignals;
}) {
  const target: ChatTarget = useMemo(() => (chat ? { scope, chat } : scope), [scope, chat]);
  const page = variant === "page";
  // A slow answer for a chat the user has since left is dropped.
  const targetKey = chat ? `${scope}/${chat}` : scope;
  const shown = useRef(targetKey);
  shown.current = targetKey;
  const [conversation, setConversationState] = useState<Conversation | null>(null);
  // Work autopilot changed reloads the project's lists, as the user's own
  // approvals do.
  const autoApplied = useRef<number | null>(null);
  const setConversation = useCallback(
    (next: Conversation | null) => {
      setConversationState(next);
      onConversation?.(next);
      onGoalChange?.(next?.goal?.status === "active" ? next.goal : null);
      const stage = next?.launch?.stage;
      onLaunchChange?.(stage === "vision" || stage === "setup" ? stage : null);
      const applied =
        next?.actions.filter((a) => a.auto_tried && a.status === "applied").length ?? null;
      if (applied !== null && autoApplied.current !== null && applied > autoApplied.current) {
        window.dispatchEvent(new Event(WORK_CHANGED));
      }
      autoApplied.current = applied;
    },
    [onConversation, onGoalChange, onLaunchChange],
  );
  const [error, setError] = useState<string | null>(null);
  const [draft, setDraft] = useState("");
  // The top-level conversation's files are its own; a project's are the
  // project's documents.
  const attachments = useAttachments(scope);
  const [sending, setSending] = useState(false);
  // The screenshot button shows where the window can be captured; while it
  // is, the assistant hides, so the image is only the app.
  const [captureSupported, setCaptureSupported] = useState(false);
  const canCapture = screenshot && captureSupported;
  const [capturing, setCapturing] = useState(false);
  useEffect(() => onCapturingChange?.(capturing), [capturing, onCapturingChange]);
  const [askChange, setAskChange] = useState(false);
  useEffect(() => {
    webviewSnapshotSupported()
      .then(setCaptureSupported)
      .catch(() => setCaptureSupported(false));
  }, []);
  const takeScreenshot = async () => {
    setError(null);
    try {
      const shot = await captureScreenshot({
        hide: setCapturing,
        painted: nextPaint,
        snapshot: webviewSnapshot,
      });
      attachments.add([shot]);
      setAskChange(true);
      composerRef.current?.focus();
    } catch (e) {
      setError(`The screenshot could not be taken: ${describe(e)}`);
    }
  };
  const [notesOpen, setNotesOpen] = useState(false);
  const chatRef = useRef<HTMLDivElement | null>(null);
  const composerRef = useRef<HTMLTextAreaElement | null>(null);
  // Set when the drawer is opened with a started message ("I'd like a new
  // app: "), so the cursor lands after it, ready to type.
  const caretToEnd = useRef(false);

  // What the user must set up before the assistant can answer (ADR-027): an
  // agent, then the assistant's choice of one. The Agents page opens over the
  // drawer, so it works in an app's own window too.
  const agents = useAgentList();
  const [settings, setSettings] = useState<AssistantSettings | null>(null);
  const guidance = assistantGuidance(agents, settings);
  const [setUp, setSetUp] = useState<null | "agents" | "assistant">(null);

  // How the assistant works in the project (ADR-027). Where it builds
  // everything, work it applies by itself shows as a line, not a card to
  // approve. Read when the scope changes and when the assistant reloads.
  const [mode, setMode] = useState<AssistantMode | null>(null);
  const loadMode = useCallback(() => {
    if (scope === "commitarium") {
      setMode(null);
      return;
    }
    getProject(scope)
      .then((project) => setMode(assistantMode(project)))
      .catch(() => undefined);
  }, [scope]);
  useEffect(() => {
    setMode(null);
    loadMode();
    window.addEventListener(ASSISTANT_CHANGED, loadMode);
    return () => window.removeEventListener(ASSISTANT_CHANGED, loadMode);
  }, [loadMode]);
  const builds = mode === "builds";

  // Vision items by ID, labelled "P0.2 Title", for the cards.
  const [visionItems, setVisionItems] = useState<Map<string, string>>(new Map());
  // How the project's vision is planned (planning mode, ADR-039): null
  // without a vision.
  const [planned, setPlanned] = useState<{ planning?: VisionPlanning } | null>(null);
  const showVision = useCallback((vision: Vision) => {
    setVisionItems(visionLabels(vision.plan.priorities));
    setPlanned({ planning: vision.planning });
  }, []);
  // The project's work orders by ID, for the live cards of the work the
  // assistant created or started.
  const live = work !== undefined;
  const [orders, setOrders] = useState<Map<string, Feature>>(new Map());
  const load = useCallback(async () => {
    getAssistantSettings()
      .then(setSettings)
      .catch(() => undefined);
    try {
      const next = await getConversation(target);
      if (shown.current !== targetKey) return;
      setConversation(next);
      setError(null);
    } catch (e) {
      if (shown.current === targetKey) setError(describe(e));
    }
    if (scope !== "commitarium") {
      getVision(scope)
        .then(showVision)
        .catch(() => {
          setVisionItems(new Map());
          setPlanned(null);
        });
      if (live) {
        listFeatures(scope)
          .then((features) => setOrders(new Map(features.map((f) => [f.id, f]))))
          .catch(() => undefined);
      }
    } else {
      setPlanned(null);
    }
  }, [scope, target, targetKey, live, setConversation, showVision]);

  // In Developer mode, planning is a switch on the chat (ADR-039): it
  // starts, resumes, or moves here, and stops with the plan kept as it is.
  // The chat stays where it is.
  const chatId = chat ?? conversation?.chat;
  const planning = chatPlanning(planned, chatId);
  const plan = usePlanSwitch(scope, projectName ?? "", chatId, planning, {
    onChanged: (vision) => (vision ? showVision(vision) : setPlanned(null)),
    onError: setError,
  });
  // The composer's + menu and /plan switch plan mode in a Developer mode
  // project's chat, as the cards' Plan mode button does.
  const planInComposer =
    scope !== "commitarium" && developerModeOn() && (planning.kind !== "none" || !!projectName);

  useEffect(() => {
    const reload = () => void load();
    window.addEventListener(ASSISTANT_CHANGED, reload);
    // Live work order cards follow the work as it changes.
    if (live) window.addEventListener(WORK_CHANGED, reload);
    return () => {
      window.removeEventListener(ASSISTANT_CHANGED, reload);
      window.removeEventListener(WORK_CHANGED, reload);
    };
  }, [load, live]);

  // Opened on an item (ADR-035): the conversation is focused on it and the
  // message that starts it is sent. One for a project the app is about to
  // open waits until the drawer follows it there.
  const pendingFocus = useRef<{ focus: AssistantFocus; send?: string; scope?: string } | null>(
    null,
  );
  const focusOn = useCallback(
    async (focus: AssistantFocus, send?: string) => {
      setError(null);
      try {
        setConversation(await setAssistantFocus(target, focus));
        if (send) setConversation(await sendToAssistant(target, send, crypto.randomUUID()));
      } catch (e) {
        setError(describe(e));
      }
    },
    [target, setConversation],
  );
  useEffect(() => {
    const pending = pendingFocus.current;
    if (!pending || (pending.scope && pending.scope !== scope)) return;
    pendingFocus.current = null;
    void focusOn(pending.focus, pending.send);
  }, [scope, focusOn]);

  useEffect(() => {
    const openWith = (event: Event) => {
      const detail = (
        event as CustomEvent<{
          draft?: string;
          focus?: AssistantFocus;
          send?: string;
          scope?: string;
        }>
      ).detail;
      if (detail?.draft) {
        setDraft(detail.draft);
        caretToEnd.current = true;
      }
      if (detail?.focus) {
        if (detail.scope && detail.scope !== scope) {
          pendingFocus.current = { focus: detail.focus, send: detail.send, scope: detail.scope };
        } else {
          void focusOn(detail.focus, detail.send);
        }
      }
      onOpenChange?.(true);
    };
    window.addEventListener(OPEN_ASSISTANT, openWith);
    return () => window.removeEventListener(OPEN_ASSISTANT, openWith);
  }, [onOpenChange, scope, focusOn]);

  useEffect(() => {
    const composer = composerRef.current;
    if (!caretToEnd.current || !open || !composer) return;
    caretToEnd.current = false;
    composer.focus();
    composer.setSelectionRange(draft.length, draft.length);
  }, [draft, open]);

  useEffect(() => {
    setConversation(null);
    setNotesOpen(false);
    setOrders(new Map());
    void load();
  }, [load, setConversation]);
  // A page's message is for the chat it was written in.
  useEffect(() => {
    if (page) setDraft("");
  }, [page, targetKey]);

  const running = conversation?.status === "running";
  // Quickly while it answers; slowly otherwise, for the turns that project
  // events start on their own.
  useEffect(() => {
    const timer = setInterval(() => void load(), running ? POLL_MS : IDLE_POLL_MS);
    return () => clearInterval(timer);
  }, [running, load]);

  const count = (conversation?.messages.length ?? 0) + (conversation?.queued?.length ?? 0);
  // The chat stays at its end while what it shows grows (cards and work
  // orders fill in after the messages) or its box shrinks, until the user
  // scrolls up to read. Only the user's own scrolling unpins it: layout
  // shifts scroll too, and WebKit has no scroll anchoring.
  const pinned = useRef(true);
  useEffect(() => {
    pinned.current = true;
  }, [targetKey]);
  useEffect(() => {
    const el = chatRef.current;
    if (!el) return;
    pinned.current = true;
    el.scrollTop = el.scrollHeight;
  }, [count, running, open, notesOpen]);
  const loaded = conversation !== null;
  useEffect(() => {
    const el = chatRef.current;
    if (!el || !loaded) return;
    const stick = () => {
      if (pinned.current) el.scrollTop = el.scrollHeight;
    };
    const resized = new ResizeObserver(stick);
    const watch = () => {
      resized.disconnect();
      resized.observe(el);
      for (const child of el.children) resized.observe(child);
      stick();
    };
    const added = new MutationObserver(watch);
    added.observe(el, { childList: true });
    watch();
    const atEnd = () => el.scrollHeight - el.scrollTop - el.clientHeight < 48;
    // Dragging the scrollbar: its scrolls are the user's.
    let dragging = false;
    const onScroll = () => {
      if (dragging) pinned.current = atEnd();
      else if (atEnd()) pinned.current = true;
    };
    const onWheel = (e: WheelEvent) => {
      if (e.deltaY < 0) pinned.current = false;
      else if (atEnd()) pinned.current = true;
    };
    const onKey = (e: KeyboardEvent) => {
      if (["ArrowUp", "PageUp", "Home"].includes(e.key)) pinned.current = false;
    };
    const onDown = () => {
      dragging = true;
    };
    const onUp = () => {
      dragging = false;
    };
    el.addEventListener("scroll", onScroll, { passive: true });
    el.addEventListener("wheel", onWheel, { passive: true });
    el.addEventListener("keydown", onKey);
    el.addEventListener("pointerdown", onDown);
    window.addEventListener("pointerup", onUp);
    return () => {
      resized.disconnect();
      added.disconnect();
      el.removeEventListener("scroll", onScroll);
      el.removeEventListener("wheel", onWheel);
      el.removeEventListener("keydown", onKey);
      el.removeEventListener("pointerdown", onDown);
      window.removeEventListener("pointerup", onUp);
    };
  }, [loaded]);

  const canSend = !!draft.trim() || attachments.items.length > 0;
  // A message from a control rather than the composer (Write the plan)
  // leaves the user's draft as it is.
  const send = async (fixed?: string) => {
    let text = fixed ?? draft.trim();
    if (!fixed && !canSend) return;
    // /plan switches plan mode; /plan with a message turns it on and sends
    // the message (ADR-039).
    const command = planInComposer && !fixed ? planCommand(text) : null;
    if (command) {
      setError(null);
      if (!command.message) {
        if (await plan.toggle()) setDraft("");
        return;
      }
      // The message says what to plan, and its turn hears planning start.
      if (!(await plan.start({ withMessage: true }))) return;
      text = command.message;
    }
    setSending(true);
    setError(null);
    try {
      const names = fixed ? [] : await attachments.upload();
      setConversation(await sendToAssistant(target, text, crypto.randomUUID(), names));
      if (!fixed) {
        setDraft("");
        attachments.clear();
      }
      setAskChange(false);
    } catch (e) {
      if (e instanceof ApiError && (e.code === "no_agents" || e.code === "assistant_not_set_up")) {
        // The guidance shows what to set up; the message waits in the box.
        void reloadAgents().catch(() => undefined);
        void load();
      } else {
        setError(describe(e));
      }
    } finally {
      setSending(false);
    }
  };

  // The Plan card's and the Plan page's Write the plan send here, in the
  // chat that plans.
  const writePlan = useRef<() => void>(() => undefined);
  useEffect(() => {
    writePlan.current = () => {
      if (planning.kind === "here" && !sending) void send(WRITE_THE_PLAN);
    };
  });
  useEffect(() => {
    const write = () => writePlan.current();
    window.addEventListener(WRITE_PLAN, write);
    return () => window.removeEventListener(WRITE_PLAN, write);
  }, []);

  // What each plan update settled and changed, said in its line; one that
  // only moved the open questions or the step is not said.
  const changes = planChanges(conversation?.actions ?? []);
  const byMessage = new Map<string, AssistantAction[]>();
  for (const action of conversation?.actions ?? []) {
    if (action.type === "update_plan" && !planChangeShows(changes.get(action.id))) continue;
    byMessage.set(action.message_id, [...(byMessage.get(action.message_id) ?? []), action]);
  }

  // Questions the user has since written past fade, still answerable.
  const passedOver = passedOverQuestions(conversation?.messages ?? []);

  // A created app or project that did not start keeps its card, with Try
  // again. Where Mayka builds everything, what Commitarium applies by itself
  // is a line from the moment she proposes it, with no card to approve in
  // the gap before it is applied.
  const asLine = (action: AssistantAction) =>
    !action.start_failed &&
    (builds && action.status === "proposed"
      ? autoAppliedInBuilds(action, { goal: conversation?.goal })
      : (builds ||
          action.type === "answer_helper" ||
          action.type === "research" ||
          action.type === "update_plan") &&
        appliesItself(action));
  // A turn's work actions fold into one summary line, and the autopilot
  // lines that repeat them are not shown again.
  const turnPieces = new Map(
    [...byMessage].map(([messageId, actions]) => [messageId, groupTurn(actions, asLine)]),
  );
  const groupedTitles = new Set(
    [...turnPieces.values()].flatMap((pieces) =>
      pieces.flatMap((piece) =>
        piece.kind === "group" ? piece.items.map((item) => item.title.toLowerCase()) : [],
      ),
    ),
  );
  // The latest written plan's "Plan drafted · Open" stays in view.
  const latestPlanDocument = [...(conversation?.actions ?? [])]
    .reverse()
    .find(
      (action) =>
        action.type === "update_plan" &&
        action.status === "applied" &&
        !!changes.get(action.id)?.document,
    )?.id;
  // A work order created and then started has a card for each: its line
  // shows once, at its latest card, beside the latest news about it.
  const lastLiveCard = new Map<string, string>();
  for (const action of conversation?.actions ?? []) {
    if (isLiveOrder(action)) lastLiveCard.set(action.work_order_id, action.id);
  }
  const turnEntries = (messageId: string): QuietEntry<ReactElement>[] =>
    (turnPieces.get(messageId) ?? [])
      .map((piece): QuietEntry<ReactElement> | null => {
        if (piece.kind === "group") {
          const line = groupLine(piece);
          return {
            quiet: line,
            node: (
              <AppliedGroup
                key={piece.actions[0].id}
                summary={piece.summary}
                items={piece.items}
                tone={line.tone}
                onOpenOrder={onOpenOrder}
              />
            ),
          };
        }
        const action = piece.action;
        if (work && isLiveOrder(action)) {
          const id = action.work_order_id;
          if (lastLiveCard.get(id) !== action.id) return null;
          const order = orders.get(id);
          const running = work.running.has(id);
          const waiting = !running && work.waiting.has(id);
          const title = order?.title ?? (appliedWhat(action.title) || action.title || id);
          const state = waiting ? "Waits for you" : order ? ORDER_STATE_LABEL[order.state] : "";
          const line = orderLine(title, state, {
            running,
            waiting,
            done: order?.state === "completed" || order?.state === "cancelled",
          });
          return {
            quiet: line,
            node: (
              <QuietRow
                key={action.id}
                tone={line.tone}
                pulse={running}
                title={action.reason ? `Why: ${action.reason}` : undefined}
              >
                {onOpenOrder ? (
                  <button className="quiet__order" onClick={() => onOpenOrder(id)}>
                    {line.text} ›
                  </button>
                ) : (
                  line.text
                )}
              </QuietRow>
            ),
          };
        }
        if (!asLine(action)) return { block: renderAction(action) };
        const running =
          action.type === "research" && (conversation?.researching ?? []).includes(action.title);
        const change = changes.get(action.id);
        const line = appliedLine(action, change, running);
        if (action.id === latestPlanDocument) line.pinned = true;
        return {
          quiet: line,
          node: (
            <AppliedLine
              key={action.id}
              action={action}
              line={line}
              change={change}
              running={running}
            />
          ),
        };
      })
      .filter((entry): entry is QuietEntry<ReactElement> => entry !== null);

  // A card for what is not a quiet line: an action card.
  const renderAction = (action: AssistantAction) => (
    <ActionCard
      key={action.id}
      scope={target}
      action={action}
      onChanged={setConversation}
      onMoved={onMoved}
      onOpenOrder={onOpenOrder}
      visionLabel={visionItems.get(action.vision_item_id)}
      plain={builds}
    />
  );

  // The chat's rows in order: messages and cards as they are, and the quiet
  // lines between them deduplicated and folded into runs (quietLines.ts).
  const chatEntries: QuietEntry<ReactElement>[] = [];
  for (const message of conversation?.messages ?? []) {
    if (message.role === "event") {
      if (foldedEvent(message.text, groupedTitles)) continue;
      if (conversation?.failure?.message_id === message.id) {
        chatEntries.push({
          block: (
            <div key={message.id}>
              <QuietRow tone="failed">{message.text}</QuietRow>
              {!running && (
                <TurnFailureControls
                  detail={conversation.failure.detail}
                  developer={developerModeOn()}
                  onRetry={() =>
                    retryAssistantTurn(target)
                      .then(setConversation)
                      .catch((e) => setError(describe(e)))
                  }
                />
              )}
            </div>
          ),
        });
      } else {
        const line = eventLine(message.text);
        chatEntries.push({ quiet: line, node: <QuietText key={message.id} line={line} /> });
      }
    } else {
      chatEntries.push({
        block: (
          <div
            key={message.id}
            className={`msg ${message.role === "user" ? "msg--user" : "msg--lead"}`}
          >
            <span className="msg__who">{message.role === "user" ? "You" : "Mayka"}</span>
            <div className="msg__text">
              <Markdown text={message.text} />
              <SentAttachments owner={scope} names={message.attachments} />
            </div>
          </div>
        ),
      });
      if (message.role === "assistant" && !!message.questions?.length) {
        chatEntries.push({
          block: (
            <QuestionCards
              key={`${message.id}:questions`}
              target={chatId ? { scope, chat: chatId } : null}
              messageId={message.id}
              questions={message.questions}
              passedOver={passedOver.has(message.id)}
              onChanged={setConversation}
            />
          ),
        });
      }
    }
    // A helper's result comes back as a card under its event line.
    chatEntries.push(...turnEntries(message.id));
  }
  const chatRows: ReactNode[] = [];
  let quietStack: ReactElement[] = [];
  const flushQuiet = () => {
    if (quietStack.length > 0) {
      chatRows.push(
        <div key={`quiet:${quietStack[0].key}`} className="quiet-stack">
          {quietStack}
        </div>,
      );
    }
    quietStack = [];
  };
  for (const segment of foldQuiet(dropRepeats(chatEntries))) {
    if (segment.kind === "block") {
      flushQuiet();
      chatRows.push(segment.node);
    } else if (segment.kind === "line") {
      quietStack.push(segment.node);
    } else {
      quietStack.push(
        <QuietRun
          key={`run:${segment.lines[0].node.key}`}
          summary={segment.summary}
          tone={segment.tone}
        >
          {segment.lines.map((item) => item.node)}
        </QuietRun>,
      );
    }
  }
  flushQuiet();

  // The mode and planning pills, and the planning toggle.
  const meta = (
    <>
      {mode && (
        <span
          className="pill assistant-drawer__mode"
          title={
            builds
              ? "It starts and finishes its own work without asking. Change this in the project's settings."
              : "It suggests; nothing happens until you approve. Change this in the project's settings."
          }
        >
          {assistantModeNote(mode)}
        </span>
      )}
      {planning.kind === "here" && (
        <span
          className="pill pill--planning assistant-drawer__mode"
          title={
            planning.round
              ? "You're planning this project's next round with Mayka in this chat. What is built stays, and nothing new is built until you press Build it."
              : "You're planning this project with Mayka in this chat. Nothing is built until you press Build it."
          }
        >
          {planning.round ? "Planning next round" : "Planning"}
        </span>
      )}
      {planSwitch && planning.kind !== "none" && developerModeOn() && (
        <button
          className="ghost"
          onClick={() => void plan.toggle()}
          disabled={plan.busy}
          title={
            planning.kind === "here"
              ? "Stop planning: keep the plan for next time, or discard it. Nothing is built."
              : planning.kind === "elsewhere"
                ? "Move planning to this chat. The plan stays as it is; the other chat stops planning."
                : "Plan this project with Mayka in this chat. Nothing is built until you press Build it."
          }
        >
          {planning.kind === "here"
            ? "Stop planning"
            : planning.kind === "elsewhere"
              ? "Plan here"
              : planning.kind === "paused"
                ? "Resume planning"
                : "Plan"}
        </button>
      )}
    </>
  );
  const notesButton = (
    <button
      className={`ghost ${notesOpen ? "ghost--on" : ""}`}
      onClick={() => setNotesOpen((v) => !v)}
    >
      Notes
    </button>
  );

  const header = page ? (
    <header className="chat-view__head">
      <h1 className="chat-view__title" title={conversation?.title || title}>
        {conversation?.title || title}
      </h1>
      {chips}
      {meta}
      <span className="chat-view__spacer" />
      {notesButton}
      {actions}
    </header>
  ) : (
    <header className="assistant-drawer__head">
      {/* The project is the title: the drawer itself is the assistant. */}
      <div className="assistant-drawer__top">
        <strong className="assistant-drawer__title" title={title}>
          {title}
        </strong>
        {notesButton}
      </div>
      {(mode || planning.kind !== "none") && <div className="assistant-drawer__meta">{meta}</div>}
    </header>
  );

  const body = !conversation ? (
    error ? (
      <div className="banner banner--error">{error}</div>
    ) : (
      <p className="muted">Loading…</p>
    )
  ) : notesOpen ? (
    <Notes scope={scope} notes={conversation.notes} onSaved={setConversation} />
  ) : (
    <>
      <div className={`chat ${page ? "chat-view__chat" : "assistant-drawer__chat"}`} ref={chatRef}>
        {conversation.messages.length === 0 && (
          <p className="muted assistant__empty">
            {scope === "commitarium"
              ? page
                ? "Hi, I'm Mayka. Talk it through with me: research, sketch, or explain. When it turns into something to build, make it a project."
                : "Hi, I'm Mayka. Ask about your projects. Open a project to have me draft work there."
              : builds
                ? "Hi, I'm Mayka. Ask for a change and I build it, along with work orders you add yourself. I ask before premium models."
                : "Hi, I'm Mayka. Ask about the project, what to build next, or have me draft work orders. I propose; nothing happens until you approve."}
          </p>
        )}
        {chatRows}
        {guidance && (
          <div className="banner banner--info assistant__setup">
            <span>{GUIDANCE[guidance].text}</span>
            <button
              className="primary"
              onClick={() => setSetUp(guidance === "add_agent" ? "agents" : "assistant")}
            >
              {GUIDANCE[guidance].action}
            </button>
          </div>
        )}
        {running && (
          <AssistantActivityLine conversation={conversation} developer={developerModeOn()} />
        )}
        {!running && conversation.sign_in?.needs_user && (
          <SignInNotice
            agentId={conversation.sign_in.agent}
            agentName={agentName(agents ?? [], conversation.sign_in.agent)}
          />
        )}
        {(conversation.queued ?? []).map((message) => (
          <div key={message.id} className="assistant__turn assistant__turn--queued">
            <div className="msg msg--user">
              <span className="msg__who">You · queued</span>
              <div className="msg__text">
                <Markdown text={message.text} />
                <SentAttachments owner={scope} names={message.attachments} />
              </div>
            </div>
            <p className="muted note assistant__queued-note">Mayka answers when she's free.</p>
          </div>
        ))}
      </div>
      {error && <div className="banner banner--error">{error}</div>}
      {planning.kind === "elsewhere" && (
        <p className="muted assistant__planning-elsewhere">
          {"This project is being planned in "}
          {onOpenChat ? (
            <button className="linkish" onClick={() => onOpenChat(planning.chat)}>
              {chatName?.(planning.chat) || "another chat"}
            </button>
          ) : (
            chatName?.(planning.chat) || "another chat"
          )}
          . Nothing new is built from the plan until you press Build it there.
        </p>
      )}
      <div
        className={`composer${attachments.dragging ? " composer--drop" : ""}`}
        {...attachments.dropProps}
      >
        {conversation.focus && (
          <div className="assistant__focus">
            <span className="pill" title="What this conversation is about">
              About: {conversation.focus.label}
            </span>
            <button
              className="ghost"
              aria-label="Clear what this conversation is about"
              onClick={() =>
                void setAssistantFocus(target, null)
                  .then(setConversation)
                  .catch((e) => setError(describe(e)))
              }
            >
              ×
            </button>
          </div>
        )}
        {attachments.problem && <p className="note composer__problem">{attachments.problem}</p>}
        <AttachmentChips
          items={attachments.items}
          onRemove={attachments.remove}
          disabled={sending}
        />
        {planInComposer && draft.startsWith("/") && (
          <p className="muted note composer__hint">
            <code>/plan</code> turns plan mode {planning.kind === "here" ? "off" : "on"};{" "}
            <code>/plan</code> and a message plans it in this chat.
          </p>
        )}
        <ComposerBox
          textareaRef={composerRef}
          value={draft}
          autoFocus
          placeholder={
            askChange
              ? SCREENSHOT_PROMPT
              : running
                ? "Mayka is busy; write anyway, she answers next…"
                : page && scope === "commitarium"
                  ? "Talk it through…"
                  : "Ask Mayka…"
          }
          onChange={setDraft}
          onPaste={attachments.onPaste}
          onKeyDown={(e) => {
            if (e.key === "Escape") onOpenChange?.(false);
            if (e.key !== "Enter" || e.shiftKey) return;
            e.preventDefault();
            if (!sending && !plan.busy && !guidance && canSend) void send();
          }}
          disabled={sending}
          onFiles={attachments.add}
          options={[
            ...(canCapture
              ? [
                  {
                    label: "Screenshot the app",
                    hint: "Show Mayka what to change",
                    onSelect: () => void takeScreenshot(),
                  },
                ]
              : []),
            ...(planInComposer
              ? [
                  {
                    label: planMenuLabel(planning),
                    hint:
                      planning.kind === "here"
                        ? "Keep the plan for next time, or discard it"
                        : "Plan it with Mayka before anything is built",
                    onSelect: () => void plan.toggle(),
                  },
                ]
              : []),
            ...(planInComposer && planning.kind === "here"
              ? [
                  planPhase(planned?.planning) === "drafted"
                    ? {
                        label: "Open the plan",
                        hint: "Review the draft, then Build it",
                        onSelect: () => window.dispatchEvent(new Event(OPEN_PLAN)),
                      }
                    : {
                        label: "Write the plan",
                        hint: "Have Mayka write the first draft now",
                        onSelect: () => void send(WRITE_THE_PLAN),
                      },
                ]
              : []),
          ]}
          onSubmit={() => void send()}
          submitDisabled={sending || plan.busy || !!guidance || !canSend}
          submitLabel={sending ? "Sending…" : "Send"}
        />
      </div>
    </>
  );

  return (
    <>
      {page ? (
        <section className="chat-view" aria-label="Chat">
          {header}
          <div className="chat-view__body">{body}</div>
        </section>
      ) : (
        open && (
          <aside
            className={`assistant-drawer ${docked ? "assistant-drawer--docked" : ""} ${capturing ? "assistant--capturing" : ""}`}
            aria-label="Mayka"
          >
            {header}
            {body}
          </aside>
        )
      )}
      <StopPlanDialog projectId={scope} plan={plan} />
      {setUp && (
        <Agents
          focus={setUp === "assistant" ? "assistant" : undefined}
          onClose={() => {
            setSetUp(null);
            void reloadAgents().catch(() => undefined);
            void load();
          }}
        />
      )}
    </>
  );
}

// A work order the assistant created or started, as it is now (ADR-038): it
// pulses while it runs, shows amber while it waits for the user, and opens
// the work order's page.
function ActionCard({
  scope,
  action,
  onChanged,
  onMoved,
  onOpenOrder,
  visionLabel,
  plain,
}: {
  scope: ChatTarget;
  action: AssistantAction;
  /** "P0.2 Title" for the vision item a new work order delivers. */
  visionLabel?: string;
  /** Where the assistant builds everything, a card is only what needs the
   * user: one plain question with one action, its details on hover. */
  plain?: boolean;
  onChanged: (conversation: Conversation) => void;
  onMoved?: (scope: string, chat: string) => void;
  onOpenOrder?: (featureId: string) => void;
}) {
  const modelName = useModelName();
  const agents = useAgents();
  const modelsFor = useCatalogModels();
  const askBefore = useAskBefore();
  const [confirmed, setConfirmed] = useState(false);
  const [busy, setBusy] = useState<null | "approve" | "start" | "dismiss">(null);
  const [error, setError] = useState<string | null>(null);
  const pending = action.status === "proposed";
  // A new or started work order's agents and models are the user's to
  // change before approving (ADR-027); another agent moves its model to one
  // its provider offers. Premium is judged on what they choose.
  const choosable =
    pending && (action.type === "create_work_order" || action.type === "start_work_order");
  const proposed: Record<AgentRole, RoleChoice> = {
    lead: { agent: action.lead_agent, model: action.lead_model },
    reviewer: { agent: action.reviewer_agent, model: action.reviewer_model },
  };
  const [chosen, setChosen] = useState(proposed);
  const roles = (["lead", "reviewer"] as const).filter((role) => action[`${role}_model`]);
  const premium = choosable
    ? roles.map((role) => chosen[role].model).filter((model) => isPremium(model, askBefore))
    : (action.premium ?? []);
  // An agent whose models are not known yet leaves its model to choose.
  const unchosen = choosable && roles.some((role) => !chosen[role].model);

  const run = async (kind: NonNullable<typeof busy>, fn: () => Promise<Conversation>) => {
    setBusy(kind);
    setError(null);
    try {
      const conversation = await fn();
      const from = typeof scope === "string" ? scope : scope.scope;
      if (onMoved && conversation.chat && conversation.scope !== from) {
        // The chat now lives in what the card created, and shows its work
        // there: the shell opens it rather than another window.
        window.dispatchEvent(new Event(WORK_CHANGED));
        onMoved(conversation.scope, conversation.chat);
        return;
      }
      onChanged(conversation);
      if (kind !== "dismiss") {
        // An approved action may have created or started work: the open
        // project's lists reload, and the app goes to what changed.
        window.dispatchEvent(new Event(WORK_CHANGED));
        const applied = conversation.actions?.find((a) => a.id === action.id);
        if (applied?.start_failed) {
          // It stays here, showing why, with Try again.
        } else if (applied?.type === "create_app" && applied.project_id) {
          void openAppWindow(applied.project_id, applied.title);
        } else if (applied?.type === "plan_project" && applied.project_id) {
          window.dispatchEvent(new CustomEvent(OPEN_PROJECT, { detail: applied.project_id }));
        } else if (applied?.type === "start_planning") {
          // Plan mode is on in this chat: the cards and the chat show it.
          window.dispatchEvent(new Event(ASSISTANT_CHANGED));
        } else if (applied?.type === "add_vision_item") {
          window.dispatchEvent(new Event(OPEN_VISION));
        } else if (applied?.work_order_id) {
          onOpenOrder?.(applied.work_order_id);
        }
      }
    } catch (e) {
      setError(describe(e));
    } finally {
      setBusy(null);
    }
  };
  const approve = (start: boolean) =>
    run(start ? "start" : "approve", () =>
      approveAssistantAction(scope, action.id, {
        start,
        confirm_premium: confirmed,
        ...(choosable ? choiceOverrides(proposed, chosen) : {}),
      }),
    );

  const heading =
    action.type === "create_work_order"
      ? `New work order: ${action.title}`
      : action.type === "start_work_order"
        ? "Start a work order"
        : action.type === "add_vision_item"
          ? `Add to the vision as P${action.priority}: ${action.title}`
          : action.type === "start_autopilot"
            ? `Autopilot: finish P${action.priority}`
            : action.type === "continue_work_order"
              ? "Continue a work order"
              : action.type === "recover_work_order"
                ? action.how === "back_to_ready"
                  ? "Start a work order over"
                  : "Recover a work order"
                : action.type === "close_work_order"
                  ? "Close a work order"
                  : action.type === "create_app"
                    ? `New app: ${action.title}`
                    : action.type === "plan_project"
                      ? `Plan a project: ${action.title}`
                      : action.type === "set_up_preview"
                        ? "Set up the app's preview"
                        : action.type === "write_vision"
                          ? "Write the vision"
                          : action.type === "set_up_stack"
                            ? "Set up the stack"
                            : action.type === "answer_helper"
                              ? "Answer a question about the work"
                              : action.type === "write_brief"
                                ? "Write the brief for a work order"
                                : action.type === "accept_brief"
                                  ? `Brief: ${action.title}`
                                  : action.type === "design_stack"
                                    ? "Design the stack"
                                    : action.type === "apply_stack"
                                      ? "Suggested stack"
                                      : action.type === "research"
                                        ? `Look up: ${action.title}`
                                        : action.type === "build_plan"
                                          ? "Build it"
                                          : action.type === "start_planning"
                                            ? "Plan it first"
                                            : action.type === "update_plan"
                                              ? "Update the plan"
                                              : "Switch models";
  const models = [
    action.lead_model &&
      `Lead ${action.lead_agent ? agentName(agents, action.lead_agent) + " · " : ""}${modelName(action.lead_model)}`,
    action.reviewer_model &&
      `Reviewer ${action.reviewer_agent ? agentName(agents, action.reviewer_agent) + " · " : ""}${modelName(action.reviewer_model)}`,
  ].filter(Boolean);
  const blocked = (premium.length > 0 && !confirmed) || unchosen;

  if (plain && pending) {
    const what = appliedWhat(action.title) ? `“${appliedWhat(action.title)}”` : "the work order";
    const [question, label] =
      premium.length > 0
        ? [
            `Use ${premium.map(modelName).join(" and ")} for ${what}? You asked to approve it first.`,
            "Use it",
          ]
        : action.type === "recover_work_order" && action.how === "back_to_ready"
          ? [`Start ${what} over? What it has done so far is set aside.`, "Start over"]
          : action.type === "start_planning"
            ? [`Plan this first? ${action.reason}`, APPLY_LABEL[action.type]]
            : [`${APPLY_LABEL[action.type]} ${what}?`, APPLY_LABEL[action.type]];
    return (
      <div
        className="assistant-action assistant-action--proposed"
        title={[action.description, `Why: ${action.reason}`].filter(Boolean).join("\n\n")}
      >
        <p className="assistant-action__desc">{question}</p>
        {error && <div className="banner banner--error">{error}</div>}
        <div className="row">
          <button
            className="primary"
            onClick={() =>
              void run("approve", () =>
                approveAssistantAction(scope, action.id, {
                  start: true,
                  confirm_premium: premium.length > 0,
                }),
              )
            }
            disabled={busy !== null || unchosen}
          >
            {busy === "approve" ? "Applying…" : label}
          </button>
          <button
            className="ghost"
            onClick={() => void run("dismiss", () => dismissAssistantAction(scope, action.id))}
            disabled={busy !== null}
          >
            Not now
          </button>
        </div>
      </div>
    );
  }

  return (
    <div className={`assistant-action assistant-action--${action.status}`}>
      <div className="assistant-action__head">
        <strong>{heading}</strong>
        {!pending && (
          <span className="pill">
            {action.settled ? "No longer needed" : STATUS_LABEL[action.status]}
          </span>
        )}
      </div>
      {action.description &&
        (action.type === "accept_brief" || action.type === "apply_stack" ? (
          <div className="assistant-action__desc">
            <Markdown text={action.description} />
          </div>
        ) : (
          <p className="assistant-action__desc">
            {action.type === "start_planning" ? `Goal: ${action.description}` : action.description}
          </p>
        ))}
      {(action.type === "create_app" || action.type === "plan_project") &&
        (action.assumptions ?? []).length > 0 && (
          <div className="assistant-action__desc">
            Assumed where you didn&apos;t say:
            <ul>
              {action.assumptions?.map((assumption) => (
                <li key={assumption}>{assumption}</li>
              ))}
            </ul>
          </div>
        )}
      {action.type === "continue_work_order" && (
        <p className="assistant-action__desc">
          {action.guidance ? `Guidance: ${action.guidance}` : "Retries without guidance."}
        </p>
      )}
      {action.type === "answer_helper" && action.guidance && (
        <p className="assistant-action__desc">Answer: {action.guidance}</p>
      )}
      {action.type === "set_up_preview" && (
        <p className="assistant-action__desc">
          An agent adjusts the app&apos;s setup; it merges once Commitarium has started the app for
          real.{action.guidance ? ` What fails: ${action.guidance}` : ""}
        </p>
      )}
      <ul className="assistant-action__facts">
        {action.steps && <li>{STEPS_LABEL[action.steps]}</li>}
        {choosable
          ? roles.map((role) => {
              const label = role === "lead" ? "Lead" : "Reviewer";
              const current = chosen[role];
              const provider = agents.find((agent) => agent.id === current.agent)?.provider;
              const options = provider ? modelsFor(provider, role) : [];
              return (
                <li key={role} className="assistant-action__model">
                  {label}{" "}
                  {agents.length > 1 ? (
                    <select
                      value={current.agent}
                      onChange={(e) =>
                        setChosen((c) => ({
                          ...c,
                          [role]: chooseAgent(
                            c[role],
                            e.target.value,
                            agents,
                            modelsFor,
                            role,
                            askBefore,
                          ),
                        }))
                      }
                      disabled={busy !== null}
                      aria-label={`${label} agent`}
                    >
                      {!agents.some((agent) => agent.id === current.agent) && (
                        <option value={current.agent}>{current.agent}</option>
                      )}
                      {agents.map((agent) => (
                        <option key={agent.id} value={agent.id}>
                          {agent.name}
                        </option>
                      ))}
                    </select>
                  ) : (
                    agentName(agents, current.agent)
                  )}
                  {" · "}
                  {options.length > 0 ? (
                    <select
                      value={current.model}
                      onChange={(e) =>
                        setChosen((c) => ({ ...c, [role]: { ...c[role], model: e.target.value } }))
                      }
                      disabled={busy !== null}
                      aria-label={`${label} model`}
                    >
                      {!options.some((m) => m.id === current.model) && (
                        <option value={current.model}>
                          {current.model ? modelName(current.model) : "Choose a model"}
                        </option>
                      )}
                      {options.map((m) => (
                        <option key={m.id} value={m.id}>
                          {m.display_name}
                        </option>
                      ))}
                    </select>
                  ) : current.model ? (
                    modelName(current.model)
                  ) : (
                    <span className="muted">its models are not known yet</span>
                  )}
                </li>
              );
            })
          : models.map((line) => <li key={line as string}>{line}</li>)}
        {action.independent_tests && <li>Independent acceptance tests</li>}
        {action.type === "create_work_order" && (
          <li>
            {action.vision_item_id
              ? `Part of the vision: ${visionLabel ?? action.vision_item_id}`
              : "Outside the vision"}
          </li>
        )}
        {action.work_order_id && (
          <li>
            Work order{" "}
            {onOpenOrder ? (
              <button className="linkish" onClick={() => onOpenOrder(action.work_order_id)}>
                {action.work_order_id}
              </button>
            ) : (
              action.work_order_id
            )}
          </li>
        )}
        {action.after && (
          <li>
            {action.queued
              ? `Queued: starts once ${action.after} has merged`
              : `Best after ${action.after}: it builds on that work`}
          </li>
        )}
        {action.type === "create_work_order" && action.brief && (
          <li>Its brief is written first; you review it before it starts</li>
        )}
        {action.type === "accept_brief" && <li>Accepting makes the work order ready to start</li>}
        {action.type === "design_stack" && (
          <li>An agent suggests the tools and services the app runs on; you review it first</li>
        )}
        {action.type === "set_up_stack" && (
          <li>The stack the vision recommends, or the one the code already uses</li>
        )}
        {action.when && (
          <li>{action.when === "now" ? "Stop now and switch" : "From the next turn"}</li>
        )}
        {action.type === "start_autopilot" && (
          <li>
            Mayka creates and starts work orders for P{action.priority}'s open items without asking,
            except with premium models. You can stop it at any time.
          </li>
        )}
        {action.auto_tried && action.status === "applied" && <li>Applied by autopilot</li>}
      </ul>
      <p className="muted assistant-action__reason">Why: {action.reason}</p>
      {action.result && <p className="muted">{action.result}</p>}
      {pending && premium.length > 0 && (
        <label className="toggle assistant-action__premium">
          <input
            type="checkbox"
            checked={confirmed}
            onChange={(e) => setConfirmed(e.target.checked)}
          />
          Use {premium.map(modelName).join(" and ")} — a model you asked to approve first
        </label>
      )}
      {error && <div className="banner banner--error">{error}</div>}
      {action.status === "applied" && action.start_failed && (
        <>
          <div className="banner banner--error">{action.start_failed}</div>
          <div className="row">
            <button
              className="primary"
              onClick={() => void approve(true)}
              disabled={busy !== null}
              title="Finish starting it, on the same project"
            >
              {busy === "start" ? "Trying again…" : "Try again"}
            </button>
          </div>
        </>
      )}
      {pending && (
        <div className="row">
          {action.type === "create_work_order" && action.brief ? (
            <button
              className="primary"
              onClick={() => void approve(false)}
              disabled={busy !== null || blocked}
            >
              {busy === "approve" ? "Creating…" : "Create and write brief"}
            </button>
          ) : action.type === "accept_brief" ? (
            <>
              <button
                className="primary"
                onClick={() => void approve(false)}
                disabled={busy !== null || blocked}
              >
                {busy === "approve" ? "Accepting…" : "Accept"}
              </button>
              {onOpenOrder && (
                <button onClick={() => onOpenOrder(action.work_order_id)} disabled={busy !== null}>
                  Edit
                </button>
              )}
            </>
          ) : action.type === "create_work_order" ? (
            <>
              <button
                className="primary"
                onClick={() => void approve(true)}
                disabled={busy !== null || blocked}
              >
                {busy === "start" ? "Starting…" : "Create and start"}
              </button>
              <button onClick={() => void approve(false)} disabled={busy !== null || blocked}>
                {busy === "approve" ? "Creating…" : "Create as draft"}
              </button>
            </>
          ) : (
            <button
              className="primary"
              onClick={() => void approve(false)}
              disabled={busy !== null || blocked}
            >
              {busy === "approve" ? "Applying…" : APPLY_LABEL[action.type]}
            </button>
          )}
          <button
            className="ghost"
            onClick={() => void run("dismiss", () => dismissAssistantAction(scope, action.id))}
            disabled={busy !== null}
          >
            Dismiss
          </button>
        </div>
      )}
    </div>
  );
}

const APPLY_LABEL: Record<AssistantAction["type"], string> = {
  create_work_order: "Create",
  start_work_order: "Start",
  change_models: "Switch",
  add_vision_item: "Add to vision",
  start_autopilot: "Start autopilot",
  continue_work_order: "Continue",
  recover_work_order: "Recover",
  close_work_order: "Close",
  create_app: "Create app",
  plan_project: "Plan it",
  set_up_preview: "Set up preview",
  write_vision: "Write vision",
  set_up_stack: "Set up stack",
  answer_helper: "Send answer",
  write_brief: "Write brief",
  accept_brief: "Accept",
  design_stack: "Design stack",
  apply_stack: "Apply",
  research: "Look it up",
  update_plan: "Update",
  build_plan: "Build it",
  start_planning: "Start plan mode",
};

const STATUS_LABEL: Record<AssistantAction["status"], string> = {
  proposed: "Proposed",
  applied: "Done",
  dismissed: "Dismissed",
  failed: "Failed",
};

function Notes({
  scope,
  notes,
  onSaved,
}: {
  scope: string;
  notes: string;
  onSaved: (conversation: Conversation) => void;
}) {
  const [editing, setEditing] = useState<string | null>(null);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const save = async () => {
    if (editing === null) return;
    setSaving(true);
    setError(null);
    try {
      onSaved(await saveAssistantNotes(scope, editing));
      setEditing(null);
    } catch (e) {
      setError(describe(e));
    } finally {
      setSaving(false);
    }
  };
  return (
    <div className="assistant-drawer__notes">
      <p className="muted note">
        What Mayka keeps for herself: decisions, your preferences, and reusable work. You can edit
        them.
      </p>
      {editing === null ? (
        <>
          {notes ? <Markdown text={notes} /> : <p className="muted">No notes yet.</p>}
          <button className="ghost" onClick={() => setEditing(notes)}>
            Edit
          </button>
        </>
      ) : (
        <>
          <textarea
            className="assistant__notes-edit"
            rows={14}
            value={editing}
            onChange={(e) => setEditing(e.target.value)}
            disabled={saving}
          />
          {error && <div className="banner banner--error">{error}</div>}
          <div className="row">
            <button className="primary" onClick={() => void save()} disabled={saving}>
              {saving ? "Saving…" : "Save"}
            </button>
            <button className="ghost" onClick={() => setEditing(null)} disabled={saving}>
              Cancel
            </button>
          </div>
        </>
      )}
    </div>
  );
}

function describe(e: unknown): string {
  if (e instanceof ApiError) {
    if (e.code === "assistant_unavailable") return "Mayka is unavailable: " + e.message;
    return e.message;
  }
  return String(e);
}

function visionLabels(priorities: VisionPriority[]): Map<string, string> {
  const labels = new Map<string, string>();
  const walk = (items: VisionItem[], prefix: string) =>
    items.forEach((item, index) => {
      const label = `${prefix}.${index + 1}`;
      labels.set(item.id, `${label} ${item.title}`);
      walk(item.items ?? [], label);
    });
  priorities.forEach((p) => walk(p.items, `P${p.level}`));
  return labels;
}

// The model families the user asked to approve first, fetched once.
let askBeforeCache: Promise<string[]> | null = null;

function useAskBefore(): string[] {
  const [families, setFamilies] = useState<string[]>(["fable", "astra"]);
  useEffect(() => {
    let active = true;
    askBeforeCache ??= getAssistantSettings()
      .then((settings) => settings.ask_before)
      .catch(() => {
        askBeforeCache = null;
        return ["fable", "astra"];
      });
    void askBeforeCache.then((loaded) => active && setFamilies(loaded));
    return () => {
      active = false;
    };
  }, []);
  return families;
}

function isPremium(model: string, families: string[]): boolean {
  const name = model.toLowerCase();
  return !!name && families.some((family) => family && name.includes(family.toLowerCase()));
}

// Where it builds everything, what Mayka set going, said in a quiet line;
// why, and what came of it, show on hover.
function AppliedLine({
  action,
  line,
  change,
  running,
}: {
  action: AssistantAction;
  line: QuietLine;
  /** For update_plan: what it settled and changed. */
  change?: PlanChange;
  running?: boolean;
}) {
  return (
    <QuietRow
      tone={line.tone}
      pulse={running}
      title={
        running
          ? action.reason && `Why: ${action.reason}`
          : [action.result, action.reason && `Why: ${action.reason}`].filter(Boolean).join("\n\n")
      }
      below={change && action.status === "applied" && <PlanChangeLines change={change} />}
    >
      {line.text}
      {line.planDocument && (
        <>
          {" · "}
          <button
            className="quiet__link"
            onClick={() => window.dispatchEvent(new Event(OPEN_PLAN))}
          >
            Open
          </button>
        </>
      )}
    </QuietRow>
  );
}

// A plan update's decisions and build plan changes (ADR-039), as quiet lines
// under it.
function PlanChangeLines({ change }: { change: PlanChange }) {
  return (
    <>
      {change.decided.slice(0, 3).map((decision) => (
        <p key={decision} className="quiet__sub">
          Decided: {decision}
        </p>
      ))}
      {change.decided.length > 3 && (
        <p className="quiet__sub">And {change.decided.length - 3} more decisions</p>
      )}
      {change.added.length > 0 && (
        <p className="quiet__sub">Added to the plan: {someOf(change.added)}</p>
      )}
      {change.changed.length > 0 && (
        <p className="quiet__sub">Changed in the plan: {someOf(change.changed)}</p>
      )}
      {change.removed.length > 0 && (
        <p className="quiet__sub">Taken out of the plan: {someOf(change.removed)}</p>
      )}
    </>
  );
}

/** The assistant's last turn waits for its agent's login: sign in here, and
 * the kept message is sent (LOGIN_RECOVERY_PLAN.md). */
export function SignInNotice({ agentId, agentName }: { agentId: string; agentName: string }) {
  return (
    <div className="wait-banner wait-banner--warn wait-banner--login">
      <span className="wait-banner__kind">{agentName} needs you to sign in again</span>
      <span>Your message is kept and is sent as soon as {agentName} works again.</span>
      <AgentLogin
        agentId={agentId}
        agentName={agentName}
        continuing="Mayka is picking your message back up"
      />
    </div>
  );
}
