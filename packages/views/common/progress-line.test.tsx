import { cleanup, render } from "@testing-library/react";
import { afterEach, describe, expect, it } from "vitest";
import type { Progress } from "@multica/core/types";
import { ProgressLine, progressDot } from "./progress-line";

const line = (overrides: Partial<Progress>): Progress => ({
  text: "Wiring the detail bar",
  source: "agent",
  author_type: "agent",
  updated_at: "2026-01-01T00:00:00Z",
  ...overrides,
});

afterEach(cleanup);

describe("progressDot", () => {
  it("reads the tone, falling back to the source for untoned lines", () => {
    expect(progressDot(line({ tone: "stuck" }))).toBe("stuck");
    expect(progressDot(line({ tone: "waiting", source: "close" }))).toBe("waiting");
    expect(progressDot(line({ tone: "", source: "close" }))).toBe("done");
    expect(progressDot(line({ source: "parking" }))).toBe("waiting");
    expect(progressDot(line({}))).toBe("working");
  });
});

describe("ProgressLine", () => {
  it("puts the live status before the text and colours the dot by it", () => {
    const { container } = render(<ProgressLine progress={line({ tone: "working" })} status="Last run failed" dot="stuck" />);
    expect(container.textContent).toBe("Last run failed · Wiring the detail bar");
    expect(container.querySelector(".bg-destructive")).not.toBeNull();
  });

  it("renders the live status alone and nothing when both are empty", () => {
    const { container, rerender } = render(<ProgressLine status="Agent is working" />);
    expect(container.textContent).toBe("Agent is working");
    expect(container.querySelector(".bg-blue-500")).not.toBeNull();
    rerender(<ProgressLine progress={line({ text: "  " })} />);
    expect(container.textContent).toBe("");
  });
});
