import {
  createContext,
  useCallback,
  useEffect,
  useRef,
  useState,
  type ClipboardEvent,
  type DragEvent,
} from "react";
import {
  MAX_ATTACHMENTS,
  attachmentName,
  attachmentProblem,
  isImageName,
  readAttachment,
  transferredFiles,
  uploadAttachment,
} from "../api/attachments";
import { Overlay } from "./Overlay";

// Attaching files to a message, in every composer: Cmd+V pastes a screenshot
// from the clipboard, files drop on the composer, and the composer's + picks
// them. They show as chips above the text and upload when the message is
// sent; a sent message shows them as thumbnails and file chips. Clicking an
// image, before or after sending, opens it full size.

/** Whose documents a message's files are: the project the view belongs to. */
export const AttachmentOwner = createContext<string | null>(null);

/** Says a project's documents changed, so a list of them reloads. */
export const DOCUMENTS_CHANGED = "commitarium:documents-changed";

interface Pending {
  id: string;
  file: File;
  name: string;
  /** The image's preview, while it waits to be sent. */
  url: string | null;
  /** The name it was stored under, once uploaded. */
  stored?: string;
}

export function useAttachments(owner: string | null) {
  const [items, setItems] = useState<Pending[]>([]);
  const [problem, setProblem] = useState<string | null>(null);
  const [dragging, setDragging] = useState(false);
  // Previews are freed once the composer goes.
  const live = useRef<Pending[]>([]);
  useEffect(() => {
    live.current = items;
  }, [items]);
  useEffect(
    () => () => live.current.forEach((item) => item.url && URL.revokeObjectURL(item.url)),
    [],
  );

  const add = useCallback((files: File[]) => {
    if (files.length === 0) return;
    const now = new Date();
    const problems: string[] = [];
    const added: Pending[] = [];
    for (const file of files) {
      const problem = attachmentProblem(file);
      if (problem) {
        problems.push(problem);
        continue;
      }
      const name = attachmentName(file, now);
      added.push({
        id: crypto.randomUUID(),
        file,
        name,
        url: isImageName(name) ? URL.createObjectURL(file) : null,
      });
    }
    setItems((current) => {
      const room = MAX_ATTACHMENTS - current.length;
      if (added.length > room) {
        added.slice(room).forEach((item) => item.url && URL.revokeObjectURL(item.url));
        problems.push(`A message carries at most ${MAX_ATTACHMENTS} files.`);
      }
      return [...current, ...added.slice(0, Math.max(room, 0))];
    });
    setProblem(problems.length ? problems.join(" ") : null);
  }, []);

  const remove = (id: string) =>
    setItems((current) =>
      current.filter((item) => {
        if (item.id === id && item.url) URL.revokeObjectURL(item.url);
        return item.id !== id;
      }),
    );

  const clear = () => {
    items.forEach((item) => item.url && URL.revokeObjectURL(item.url));
    setItems([]);
    setProblem(null);
  };

  /** Uploads what is not uploaded yet and answers every file's stored name.
   * A failed send keeps the uploaded ones, so sending again does not store
   * them twice. */
  const upload = async (): Promise<string[]> => {
    if (items.length === 0) return [];
    if (!owner) throw new Error("Files can't be attached here.");
    const names: string[] = [];
    for (const item of items) {
      if (!item.stored) {
        const stored = (await uploadAttachment(owner, item.file, item.name)).name;
        item.stored = stored;
        setItems((current) => current.map((c) => (c.id === item.id ? { ...c, stored } : c)));
      }
      names.push(item.stored);
    }
    window.dispatchEvent(new Event(DOCUMENTS_CHANGED));
    return names;
  };

  /** Cmd+V: files on the clipboard attach instead of pasting as text. */
  const onPaste = (e: ClipboardEvent<HTMLElement>) => {
    const files = transferredFiles(e.clipboardData);
    if (files.length === 0) return;
    e.preventDefault();
    add(files);
  };

  const carriesFiles = (e: DragEvent<HTMLElement>) =>
    Array.from(e.dataTransfer.types).includes("Files");
  const dropProps = {
    onDragOver: (e: DragEvent<HTMLElement>) => {
      if (!carriesFiles(e)) return;
      e.preventDefault();
      setDragging(true);
    },
    onDragLeave: (e: DragEvent<HTMLElement>) => {
      if (!e.currentTarget.contains(e.relatedTarget as Node | null)) setDragging(false);
    },
    onDrop: (e: DragEvent<HTMLElement>) => {
      if (!carriesFiles(e)) return;
      e.preventDefault();
      setDragging(false);
      add(transferredFiles(e.dataTransfer));
    },
  };

  return { items, add, remove, clear, upload, onPaste, dropProps, dragging, problem };
}

/** The files waiting to be sent, above the composer's text. */
export function AttachmentChips({
  items,
  onRemove,
  disabled,
}: {
  items: Pending[];
  onRemove: (id: string) => void;
  disabled?: boolean;
}) {
  const [open, setOpen] = useState<{ name: string; url: string } | null>(null);
  if (items.length === 0) return null;
  return (
    <>
      <ul className="attachments">
        {items.map((item) => (
          <li key={item.id} className="attachment" title={item.name}>
            {item.url ? (
              <button
                className="attachment__open"
                aria-label={`Show ${item.name} full size`}
                onClick={() => setOpen({ name: item.name, url: item.url! })}
              >
                <img className="attachment__thumb" src={item.url} alt="" />
              </button>
            ) : (
              <span className="attachment__icon" aria-hidden>
                📄
              </span>
            )}
            <span className="attachment__name">{item.name}</span>
            <button
              className="ghost attachment__remove"
              aria-label={`Remove ${item.name}`}
              onClick={() => onRemove(item.id)}
              disabled={disabled}
            >
              ×
            </button>
          </li>
        ))}
      </ul>
      {open && (
        <Overlay title={open.name} onClose={() => setOpen(null)}>
          <img className="attachment__large" src={open.url} alt={open.name} />
        </Overlay>
      )}
    </>
  );
}

/** A sent message's files: thumbnails that open larger, and file chips. */
export function SentAttachments({ owner, names }: { owner: string | null; names?: string[] }) {
  const [open, setOpen] = useState<{ name: string; url: string } | null>(null);
  if (!names?.length) return null;
  return (
    <>
      <ul className="attachments attachments--sent">
        {names.map((name) =>
          isImageName(name) && owner ? (
            <SentImage key={name} owner={owner} name={name} onOpen={setOpen} />
          ) : (
            <li key={name} className="attachment" title={name}>
              <span className="attachment__icon" aria-hidden>
                📄
              </span>
              <span className="attachment__name">{name}</span>
            </li>
          ),
        )}
      </ul>
      {open && (
        <Overlay title={open.name} onClose={() => setOpen(null)}>
          <img className="attachment__large" src={open.url} alt={open.name} />
        </Overlay>
      )}
    </>
  );
}

function SentImage({
  owner,
  name,
  onOpen,
}: {
  owner: string;
  name: string;
  onOpen: (image: { name: string; url: string }) => void;
}) {
  const [url, setUrl] = useState<string | null>(null);
  const [missing, setMissing] = useState(false);
  useEffect(() => {
    let made: string | null = null;
    let gone = false;
    readAttachment(owner, name)
      .then((blob) => {
        if (gone) return;
        made = URL.createObjectURL(blob);
        setUrl(made);
      })
      .catch(() => !gone && setMissing(true));
    return () => {
      gone = true;
      if (made) URL.revokeObjectURL(made);
    };
  }, [owner, name]);
  return (
    <li
      className="attachment attachment--image"
      title={missing ? `${name} is not there any more` : name}
    >
      {url ? (
        <button className="attachment__open" onClick={() => onOpen({ name, url })}>
          <img className="attachment__preview" src={url} alt={name} />
        </button>
      ) : (
        <>
          <span className="attachment__icon" aria-hidden>
            🖼
          </span>
          <span className="attachment__name">{missing ? `${name} (removed)` : name}</span>
        </>
      )}
    </li>
  );
}
