import { describe, expect, it } from "vitest";
import { reviewSkipReason } from "./review-skip";

describe("reviewSkipReason", () => {
  it("shows the server's reason on a done ticket", () => {
    expect(reviewSkipReason({ status: "done", metadata: { review_skip: " PR 已合入 " } })).toBe("PR 已合入");
  });
  it("hides a stale reason once the ticket is not done", () => {
    expect(reviewSkipReason({ status: "in_review", metadata: { review_skip: "PR 已合入" } })).toBe("");
  });
  it("ignores a missing or non-string value", () => {
    expect(reviewSkipReason({ status: "done", metadata: {} })).toBe("");
    expect(reviewSkipReason({ status: "done", metadata: { review_skip: 1 } })).toBe("");
  });
});
