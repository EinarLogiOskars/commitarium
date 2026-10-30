import { describe, expect, it } from "vitest";
import { bringInDocument, bringInMessage } from "./bringIn";

describe("bringIn", () => {
  const head = "0123456789abcdef0123456789abcdef01234567";

  it("names the commit where Pull looks for it", () => {
    const name = bringInDocument(head);
    expect(name).toBe("local-changes-0123456789ab.patch");
    const message = bringInMessage(
      { headCommitId: head, commits: 2, files: ["a"], uncommittedFiles: 0, patch: "" },
      name,
    );
    expect(message).toContain('"Bring in local changes 0123456789ab"');
    expect(message).toContain(`.commitarium/documents/${name}`);
    expect(message).toContain("2 local commits");
  });
});
