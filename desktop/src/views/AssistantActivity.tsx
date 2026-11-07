import { useEffect, useState } from "react";
import type { AssistantActivity, Conversation } from "../api/assistant";
import { Markdown } from "./Markdown";

/** How long the turn has run, as "12 s" or "3:05". */
export function elapsedLabel(startedAt: string | undefined, now: number): string {
  const started = startedAt ? Date.parse(startedAt) : NaN;
  if (Number.isNaN(started)) return "";
  const seconds = Math.max(0, Math.floor((now - started) / 1000));
  if (seconds < 60) return `${seconds} s`;
  return `${Math.floor(seconds / 60)}:${String(seconds % 60).padStart(2, "0")}`;
}

/** The step line under "thinking": plain words, or what it looked at in
 * Developer mode. */
export function stepLabel(activity: AssistantActivity | undefined, developer: boolean): string {
  if (!activity) return "";
  const step = developer && activity.detail ? activity.detail : activity.label;
  return activity.retry ? `${step} · trying again` : step;
}

// What the assistant is doing while it answers (the running turn's latest
// step) under the thinking line, with how long it has run. The step replaces
// itself as the next one comes; once the turn ends, the line goes. While she
// writes, the reply so far shows above it, and the finished message takes its
// place.
export function AssistantActivityLine({
  conversation,
  developer,
}: {
  conversation: Conversation;
  developer: boolean;
}) {
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    const timer = setInterval(() => setNow(Date.now()), 1000);
    return () => clearInterval(timer);
  }, []);
  const activity = conversation.activity;
  const elapsed = elapsedLabel(activity?.started_at ?? conversation.turn_started_at, now);
  const step = stepLabel(activity, developer);
  const reply = activity?.reply;
  return (
    <div className="assistant__activity" aria-live="polite">
      {reply && (
        <div className="msg msg--lead msg--plain assistant__draft">
          <div className="msg__text">
            <Markdown text={reply} />
          </div>
        </div>
      )}
      <p className="muted status-line live-text">
        Mayka is thinking…
        {elapsed && <span className="assistant__elapsed"> {elapsed}</span>}
      </p>
      {step && (
        <p className="muted assistant__step" title={developer ? activity?.label : undefined}>
          {step}
        </p>
      )}
    </div>
  );
}

// Under the line that says a turn could not finish: Retry runs it again,
// and in Developer mode Details shows the exact reason.
export function TurnFailureControls({
  detail,
  developer,
  onRetry,
}: {
  detail?: string;
  developer: boolean;
  onRetry: () => Promise<void>;
}) {
  const [open, setOpen] = useState(false);
  const [retrying, setRetrying] = useState(false);
  return (
    <div className="assistant__failure">
      <button
        className="ghost"
        disabled={retrying}
        onClick={() => {
          setRetrying(true);
          void onRetry().finally(() => setRetrying(false));
        }}
      >
        {retrying ? "Retrying…" : "Retry"}
      </button>
      {developer && detail && (
        <button className="ghost" aria-expanded={open} onClick={() => setOpen(!open)}>
          {open ? "Hide details" : "Details"}
        </button>
      )}
      {open && developer && detail && <pre className="assistant__failure-detail">{detail}</pre>}
    </div>
  );
}

/** What goes on in the chat's project while Mayka is not answering: the new
 * app's launch, its work orders running, or autopilot between them. Empty
 * when nothing does. Running titles are the work orders under way. */
export function liveLabel(conversation: Conversation, runningTitles: string[]): string {
  const stage = conversation.launch?.stage;
  if (stage === "vision") return "Writing the app's vision…";
  if (stage === "setup") return "Setting the app up…";
  const goal = conversation.goal?.status === "active" ? conversation.goal : null;
  const progress = goal
    ? ` · autopilot ${goal.items.filter((item) => item.done).length} of ${goal.items.length} done`
    : "";
  if (runningTitles.length === 1) return `Waiting on “${runningTitles[0]}”${progress}`;
  if (runningTitles.length > 1)
    return `Waiting on work orders · ${runningTitles.length} active${progress}`;
  if (goal) return `Autopilot is picking the next work order${progress}`;
  return "";
}

// The chat's live line while Mayka is not answering, in the same look as
// her thinking line.
export function ChatLiveLine({ text }: { text: string }) {
  return (
    <div className="assistant__activity" aria-live="polite">
      <p className="muted status-line live-text">{text}</p>
    </div>
  );
}
