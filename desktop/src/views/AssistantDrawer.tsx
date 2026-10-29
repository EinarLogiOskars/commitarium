import { useCallback, useEffect, useRef, useState } from "react";
import type { AutopilotGoal, Conversation, LaunchStage } from "../api/assistant";
import { ChatView } from "./ChatView";

export {
  ASSISTANT_CHANGED,
  OPEN_ASSISTANT,
  OPEN_PROJECT,
  OPEN_VISION,
  SignInNotice,
  WORK_CHANGED,
} from "./ChatView";

// The assistant (ASSISTANT_PLAN.md) in a drawer on the right of the window,
// opened from a round button in the top-right corner and minimized back to
// it, so the user keeps their place. Docked, the page narrows beside it;
// otherwise it lies over the page. It follows where the user is: a
// project's conversation inside a project, the top-level one elsewhere, each
// its most recently used chat. The button pulses while it answers and shows
// a dot for a reply not yet seen.
export function AssistantDrawer({
  scope,
  scopeLabel,
  onOpenOrder,
  open,
  onOpenChange: setOpen,
  docked,
  onGoalChange,
  onLaunchChange,
  screenshot = false,
}: {
  scope: string;
  scopeLabel: string;
  onOpenOrder?: (featureId: string) => void;
  open: boolean;
  onOpenChange: (open: boolean) => void;
  /** Beside the page rather than over it. */
  docked: boolean;
  /** The conversation's autopilot goal while it is on, else null. */
  onGoalChange?: (goal: AutopilotGoal | null) => void;
  /** A new app's launch stage while it is planned or set up, else null. */
  onLaunchChange?: (stage: LaunchStage | null) => void;
  /** Offers "Take a screenshot and describe what to change": over an app's
   * own window, a capture of the app as the user sees it. */
  screenshot?: boolean;
}) {
  const [conversation, setConversation] = useState<Conversation | null>(null);
  const [capturing, setCapturing] = useState(false);
  const [unseen, setUnseen] = useState(false);
  const seenReplies = useRef<number | null>(null);
  const onConversation = useCallback((next: Conversation | null) => setConversation(next), []);

  useEffect(() => {
    setUnseen(false);
    seenReplies.current = null;
  }, [scope]);

  const running = conversation?.status === "running";
  // A reply that arrives while the drawer is closed marks the button.
  const replies = conversation?.messages.filter((m) => m.role !== "user").length ?? 0;
  useEffect(() => {
    if (!conversation) return;
    if (seenReplies.current === null || open) {
      seenReplies.current = replies;
      if (open) setUnseen(false);
      return;
    }
    if (replies > seenReplies.current) setUnseen(true);
  }, [conversation, replies, open]);

  return (
    <>
      {/* One button in the corner opens and closes the drawer. */}
      <button
        className={`assistant-fab ${open ? "assistant-fab--open" : ""} ${running && !open ? "assistant-fab--working" : ""} ${capturing ? "assistant--capturing" : ""}`}
        onClick={() => setOpen(!open)}
        title={open ? "Close Mayka" : running ? "Mayka is answering" : "Talk to Mayka"}
        aria-label={open ? "Close Mayka" : "Talk to Mayka"}
        aria-expanded={open}
      >
        {open ? (
          <svg viewBox="0 0 24 24" width="18" height="18" fill="none" aria-hidden>
            <path
              d="M6 6l12 12M18 6L6 18"
              stroke="currentColor"
              strokeWidth="2"
              strokeLinecap="round"
            />
          </svg>
        ) : (
          <svg viewBox="0 0 24 24" width="20" height="20" fill="none" aria-hidden>
            <path
              d="M12 3l1.8 4.6L18.5 9l-4.7 1.6L12 15l-1.8-4.4L5.5 9l4.7-1.4L12 3zM18 15l.9 2.1 2.1.9-2.1.9L18 21l-.9-2.1L15 18l2.1-.9L18 15z"
              fill="currentColor"
            />
          </svg>
        )}
        {unseen && !open && (
          <span className="assistant-fab__dot" aria-label="A reply you have not seen" />
        )}
      </button>
      <ChatView
        variant="drawer"
        scope={scope}
        title={scopeLabel}
        open={open}
        onOpenChange={setOpen}
        docked={docked}
        onOpenOrder={onOpenOrder}
        onGoalChange={onGoalChange}
        onLaunchChange={onLaunchChange}
        onConversation={onConversation}
        onCapturingChange={setCapturing}
        screenshot={screenshot}
      />
    </>
  );
}
