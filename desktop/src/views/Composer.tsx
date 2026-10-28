import {
  useEffect,
  useLayoutEffect,
  useRef,
  useState,
  type ClipboardEvent,
  type KeyboardEvent,
  type MutableRefObject,
  type ReactNode,
} from "react";

// The message box every composer shares, on one line: the + opens what can
// go along with the message (files, a screenshot of the app), the text grows
// with what is typed up to MAX_LINES and then scrolls, and the return key
// sends. The + and the return stay on the last line as the text grows. Enter sends; the composer's own onKeyDown decides that.

const MAX_LINES = 6;

/** Something the + offers besides picking files. */
export interface ComposerOption {
  label: string;
  hint?: string;
  onSelect: () => void;
}

export function ComposerBox({
  value,
  onChange,
  onKeyDown,
  onPaste,
  placeholder,
  disabled,
  autoFocus,
  textareaRef,
  onFiles,
  filesDisabled,
  options = [],
  leading,
  onSubmit,
  submitDisabled,
  submitLabel,
  submitIcon = "send",
}: {
  value: string;
  onChange: (value: string) => void;
  onKeyDown?: (e: KeyboardEvent<HTMLTextAreaElement>) => void;
  onPaste?: (e: ClipboardEvent<HTMLTextAreaElement>) => void;
  placeholder?: string;
  disabled?: boolean;
  autoFocus?: boolean;
  textareaRef?: MutableRefObject<HTMLTextAreaElement | null>;
  /** Picking files; without it the + offers only the options. */
  onFiles?: (files: File[]) => void;
  filesDisabled?: boolean;
  options?: ComposerOption[];
  /** Controls beside the +, such as who the message goes to. */
  leading?: ReactNode;
  onSubmit: () => void;
  submitDisabled?: boolean;
  /** What the send button does, for its tooltip and screen readers. */
  submitLabel: string;
  submitIcon?: "send" | "pause";
}) {
  const text = useRef<HTMLTextAreaElement | null>(null);

  useLayoutEffect(() => {
    const el = text.current;
    if (!el) return;
    const style = getComputedStyle(el);
    const line = parseFloat(style.lineHeight) || parseFloat(style.fontSize) * 1.4;
    const max = line * MAX_LINES + parseFloat(style.paddingTop) + parseFloat(style.paddingBottom);
    el.style.height = "auto";
    el.style.height = `${Math.min(el.scrollHeight, max)}px`;
    el.style.overflowY = el.scrollHeight > max ? "auto" : "hidden";
  }, [value]);

  return (
    <div className={`composer-box${disabled ? " composer-box--disabled" : ""}`}>
      <PlusMenu onFiles={onFiles} filesDisabled={filesDisabled} options={options} />
      {leading}
      <textarea
        ref={(el) => {
          text.current = el;
          if (textareaRef) textareaRef.current = el;
        }}
        className="composer-box__text"
        rows={1}
        value={value}
        placeholder={placeholder}
        autoFocus={autoFocus}
        disabled={disabled}
        onChange={(e) => onChange(e.target.value)}
        onKeyDown={onKeyDown}
        onPaste={onPaste}
      />
      <button
        className="composer-box__send"
        onClick={onSubmit}
        disabled={submitDisabled}
        aria-label={submitLabel}
        title={submitLabel}
      >
        {submitIcon === "pause" ? <PauseIcon /> : <ReturnIcon />}
      </button>
    </div>
  );
}

function PlusMenu({
  onFiles,
  filesDisabled,
  options,
}: {
  onFiles?: (files: File[]) => void;
  filesDisabled?: boolean;
  options: ComposerOption[];
}) {
  const [open, setOpen] = useState(false);
  const root = useRef<HTMLDivElement | null>(null);
  const input = useRef<HTMLInputElement | null>(null);

  useEffect(() => {
    if (!open) return;
    const onDown = (e: MouseEvent) => {
      if (!root.current?.contains(e.target as Node)) setOpen(false);
    };
    const onKey = (e: globalThis.KeyboardEvent) => {
      if (e.key === "Escape") {
        e.stopPropagation();
        setOpen(false);
      }
    };
    window.addEventListener("mousedown", onDown);
    window.addEventListener("keydown", onKey, true);
    return () => {
      window.removeEventListener("mousedown", onDown);
      window.removeEventListener("keydown", onKey, true);
    };
  }, [open]);

  const items: ComposerOption[] = [
    ...(onFiles && !filesDisabled
      ? [
          {
            label: "Add files or photos",
            hint: "You can also paste or drop them",
            onSelect: () => input.current?.click(),
          },
        ]
      : []),
    ...options,
  ];

  return (
    <div className="composer-menu" ref={root}>
      <button
        className="composer-box__plus"
        aria-label="Add to the message"
        aria-haspopup="menu"
        aria-expanded={open}
        title="Add to the message"
        onClick={() => setOpen((was) => !was)}
        disabled={items.length === 0}
      >
        <PlusIcon />
      </button>
      {open && (
        <ul className="composer-menu__list" role="menu">
          {items.map((item) => (
            <li key={item.label}>
              <button
                role="menuitem"
                className="composer-menu__item"
                onClick={() => {
                  setOpen(false);
                  item.onSelect();
                }}
              >
                <span>{item.label}</span>
                {item.hint && <span className="composer-menu__hint">{item.hint}</span>}
              </button>
            </li>
          ))}
        </ul>
      )}
      {onFiles && (
        <input
          ref={input}
          type="file"
          multiple
          hidden
          onChange={(e) => {
            onFiles(Array.from(e.target.files ?? []));
            e.target.value = "";
          }}
        />
      )}
    </div>
  );
}

function PlusIcon() {
  return (
    <svg width="16" height="16" viewBox="0 0 16 16" aria-hidden>
      <path d="M8 3v10M3 8h10" stroke="currentColor" strokeWidth="1.6" strokeLinecap="round" />
    </svg>
  );
}

function ReturnIcon() {
  return (
    <svg width="16" height="16" viewBox="0 0 16 16" aria-hidden>
      <path
        d="M13 3.5v4.5a2 2 0 0 1-2 2H3.5M6.5 7 3.5 10l3 3"
        fill="none"
        stroke="currentColor"
        strokeWidth="1.8"
        strokeLinecap="round"
        strokeLinejoin="round"
      />
    </svg>
  );
}

function PauseIcon() {
  return (
    <svg width="16" height="16" viewBox="0 0 16 16" aria-hidden>
      <path d="M5.5 4v8M10.5 4v8" stroke="currentColor" strokeWidth="2" strokeLinecap="round" />
    </svg>
  );
}
