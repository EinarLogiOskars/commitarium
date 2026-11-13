import { describe, expect, it } from "vitest";
import { uploadProgress } from "./Attachments";

describe("the composer while files upload", () => {
  it("says which file is uploading, then that the message is sending", () => {
    expect(uploadProgress([{}, {}, {}, {}])).toBe("Uploading 1 of 4 files…");
    expect(uploadProgress([{ stored: "a.png" }, {}, {}, {}])).toBe("Uploading 2 of 4 files…");
    expect(uploadProgress([{}])).toBe("Uploading 1 of 1 file…");
    expect(uploadProgress([{ stored: "a.png" }, { stored: "b.png" }])).toBe("Sending…");
  });
});
