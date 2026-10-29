import { useEffect, useRef, useState, type KeyboardEvent } from "react";
import {
  answerAssistantQuestions,
  type AssistantQuestion,
  type Conversation,
} from "../api/assistant";
import { answerText, pickedByKey } from "./planMode";

// The assistant's questions under its reply (ADR-039): one card holding them
// as steps, each with its suggested answers as rows, the one the assistant
// would pick tagged, and "Something else…" for the user's own words. Rows
// are picked by click, or by number key and Enter while the card has focus.
// The answers are sent together once the last is given, as one message of
// the user's, so the assistant hears them all in one turn; answered, the
// card folds to a line per question.

type Pick = { option: number } | { text: string };

export function QuestionCards({
  target,
  messageId,
  questions,
  passedOver = false,
  onChanged,
}: {
  /** The chat the message is in; without one, the questions only show. */
  target: { scope: string; chat: string } | null;
  messageId: string;
  questions: AssistantQuestion[];
  /** The user has written since: the card stays answerable, but quiet. */
  passedOver?: boolean;
  onChanged: (conversation: Conversation) => void;
}) {
  const open = questions.filter((question) => !question.answer);
  const answered = questions.filter((question) => question.answer);
  if (open.length === 0) {
    return (
      <div className={`qcard qcard--answered${passedOver ? " qcard--stale" : ""}`}>
        <AnsweredLines questions={answered} />
      </div>
    );
  }
  return (
    <OpenCard
      // A new set of open questions starts over.
      key={open.map((question) => question.id).join(",")}
      target={target}
      messageId={messageId}
      open={open}
      answered={answered}
      passedOver={passedOver}
      onChanged={onChanged}
    />
  );
}

function AnsweredLines({ questions }: { questions: AssistantQuestion[] }) {
  return (
    <>
      {questions.map((question) => (
        <p key={question.id} className="qcard__answered">
          {question.text} → <strong>{answerText(question)}</strong>
        </p>
      ))}
    </>
  );
}

function OpenCard({
  target,
  messageId,
  open,
  answered,
  passedOver,
  onChanged,
}: {
  target: { scope: string; chat: string } | null;
  messageId: string;
  open: AssistantQuestion[];
  answered: AssistantQuestion[];
  passedOver: boolean;
  onChanged: (conversation: Conversation) => void;
}) {
  const card = useRef<HTMLDivElement>(null);
  const [step, setStep] = useState(0);
  const [picks, setPicks] = useState<Record<string, Pick>>({});
  const question = open[Math.min(step, open.length - 1)];
  const other = question.options.length;
  const [selected, setSelected] = useState(question.suggested);
  const [writing, setWriting] = useState(false);
  const [own, setOwn] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const disabled = busy || !target;

  // The latest card takes focus when it appears, unless the user is typing
  // somewhere: it never takes keys from the composer.
  const [latest] = useState(!passedOver);
  useEffect(() => {
    const active = document.activeElement;
    if (latest && (!active || active === document.body)) {
      card.current?.focus({ preventScroll: true });
    }
  }, [latest]);

  /** Shows a step with what was picked there, or the suggestion. */
  const show = (next: number, chosen: Record<string, Pick>) => {
    const at = open[next];
    const pick = chosen[at.id];
    setStep(next);
    setWriting(!!pick && "text" in pick);
    setOwn(pick && "text" in pick ? pick.text : "");
    setSelected(pick ? ("option" in pick ? pick.option : at.options.length) : at.suggested);
  };

  const submit = async (chosen: Record<string, Pick>) => {
    if (!target) return;
    setBusy(true);
    setError(null);
    try {
      onChanged(
        await answerAssistantQuestions(
          target,
          messageId,
          open.map((at) => ({ question: at.id, ...chosen[at.id] })),
        ),
      );
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    } finally {
      setBusy(false);
    }
  };

  const confirm = (pick: Pick) => {
    if (disabled) return;
    const chosen = { ...picks, [question.id]: pick };
    setPicks(chosen);
    const hadFocus = !!card.current?.contains(document.activeElement);
    if (step < open.length - 1) show(step + 1, chosen);
    else void submit(chosen);
    if (hadFocus) card.current?.focus({ preventScroll: true });
  };

  const choose = (index: number) => {
    setSelected(index);
    if (index === other) setWriting(true);
    else confirm({ option: index });
  };

  const onKeyDown = (e: KeyboardEvent<HTMLDivElement>) => {
    if (disabled || e.metaKey || e.ctrlKey || e.altKey) return;
    if (e.target instanceof HTMLInputElement) {
      if (e.key === "Escape") {
        setWriting(false);
        card.current?.focus({ preventScroll: true });
      }
      return;
    }
    const picked = pickedByKey(e.key, other);
    if (picked !== null) {
      e.preventDefault();
      setSelected(picked);
      setWriting(picked === other);
    } else if (e.key === "ArrowDown" || e.key === "ArrowUp") {
      e.preventDefault();
      setSelected((selected + (e.key === "ArrowDown" ? 1 : other)) % (other + 1));
      setWriting(false);
    } else if (e.key === "Enter" && e.target === card.current) {
      // On a row, Enter clicks it.
      e.preventDefault();
      if (selected === other) setWriting(true);
      else confirm({ option: selected });
    }
  };

  const textId = `${messageId}-${question.id}`;
  return (
    <div
      ref={card}
      className={`qcard${passedOver ? " qcard--stale" : ""}`}
      tabIndex={0}
      role="group"
      aria-label="Mayka's questions"
      onKeyDown={onKeyDown}
    >
      <AnsweredLines questions={answered} />
      {open.length > 1 && (
        <div className="qcard__head">
          <span>
            Question {step + 1} of {open.length}
          </span>
          {step > 0 && (
            <button className="qcard__back" onClick={() => show(step - 1, picks)} disabled={busy}>
              Back
            </button>
          )}
        </div>
      )}
      <p className="qcard__question" id={textId}>
        {question.text}
      </p>
      <ol className="qcard__options" aria-labelledby={textId}>
        {question.options.map((option, index) => (
          <li key={index}>
            <button
              className={`qcard__option${selected === index ? " qcard__option--selected" : ""}`}
              onClick={() => choose(index)}
              disabled={disabled}
              aria-current={selected === index ? "true" : undefined}
              aria-keyshortcuts={String(index + 1)}
            >
              <kbd className="qcard__key" aria-hidden>
                {index + 1}
              </kbd>
              <span className="qcard__label">{option}</span>
              {index === question.suggested && <span className="qcard__tag">Suggested</span>}
            </button>
          </li>
        ))}
        <li>
          {writing ? (
            <form
              className="qcard__other"
              onSubmit={(e) => {
                e.preventDefault();
                if (own.trim()) confirm({ text: own.trim() });
              }}
            >
              <kbd className="qcard__key" aria-hidden>
                {other + 1}
              </kbd>
              <input
                autoFocus
                value={own}
                onChange={(e) => setOwn(e.target.value)}
                placeholder="Your answer, Enter to send"
                aria-label={`Your own answer to: ${question.text}`}
                disabled={disabled}
              />
            </form>
          ) : (
            <button
              className={`qcard__option${selected === other ? " qcard__option--selected" : ""}`}
              onClick={() => choose(other)}
              disabled={disabled}
              aria-current={selected === other ? "true" : undefined}
              aria-keyshortcuts={String(other + 1)}
            >
              <kbd className="qcard__key" aria-hidden>
                {other + 1}
              </kbd>
              <span className="qcard__label">Something else…</span>
            </button>
          )}
        </li>
      </ol>
      {busy && <p className="qcard__hint">Sending…</p>}
      {error && <div className="banner banner--error">{error}</div>}
    </div>
  );
}
