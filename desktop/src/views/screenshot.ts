// "Take a screenshot and describe what to change": one click captures what
// the user sees of the app, with the assistant itself hidden, and attaches it
// to the message.

/** What the composer asks once a screenshot is attached. */
export const SCREENSHOT_PROMPT = "What should change here?";

interface Capture {
  /** Hides or shows the assistant's own panel. */
  hide: (hidden: boolean) => void;
  /** Resolves once the page has drawn itself without it. */
  painted: () => Promise<void>;
  /** The window's snapshot. */
  snapshot: () => Promise<Blob>;
}

/** Captures the window without the assistant and answers it as a pasted
 * image, which the composer names screenshot-<date>-<time>.png. The
 * assistant shows again whether or not the capture worked. */
export async function captureScreenshot({ hide, painted, snapshot }: Capture): Promise<File> {
  hide(true);
  try {
    await painted();
    const png = await snapshot();
    return new File([png], "image.png", { type: "image/png" });
  } finally {
    hide(false);
  }
}

/** Two frames: the page drew itself after the last change. */
export const nextPaint = () =>
  new Promise<void>((resolve) =>
    requestAnimationFrame(() => requestAnimationFrame(() => resolve())),
  );
