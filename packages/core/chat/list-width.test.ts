import { describe, expect, it } from "vitest";
import {
  chatListMaxWidth,
  clampChatListWidth,
  resolveChatListDrag,
  toggleChatListWidth,
} from "./list-width";

describe("clampChatListWidth", () => {
  it("lets the list take everything but the conversation's 360px", () => {
    expect(chatListMaxWidth(1200)).toBe(840);
    expect(clampChatListWidth(700, 1200)).toBe(700);
    expect(clampChatListWidth(2000, 1200)).toBe(840);
    expect(clampChatListWidth(100, 1200)).toBe(240);
  });

  it("keeps the minimum when the window cannot give both panes their floor", () => {
    expect(clampChatListWidth(400, 500)).toBe(240);
  });

  it("does not cap before the container is measured", () => {
    expect(clampChatListWidth(900, 0)).toBe(900);
    expect(clampChatListWidth(Number.NaN, 0)).toBe(300);
  });
});

describe("resolveChatListDrag", () => {
  it("snaps to the fit width when the drag passes close to it", () => {
    const near = resolveChatListDrag({ wanted: 508, containerWidth: 1200, fitWidth: 500 });
    expect(near).toEqual({ width: 500, atMax: false, atFit: true, fitGuide: 500 });

    const approaching = resolveChatListDrag({ wanted: 450, containerWidth: 1200, fitWidth: 500 });
    expect(approaching).toEqual({ width: 450, atMax: false, atFit: false, fitGuide: 500 });

    const far = resolveChatListDrag({ wanted: 400, containerWidth: 1200, fitWidth: 500 });
    expect(far.fitGuide).toBeNull();
  });

  it("stops at the limit and says so", () => {
    const drag = resolveChatListDrag({ wanted: 900, containerWidth: 1200, fitWidth: 500 });
    expect(drag).toEqual({ width: 840, atMax: true, atFit: false, fitGuide: null });
  });

  it("lets the limit win over a fit width just short of it", () => {
    const drag = resolveChatListDrag({ wanted: 900, containerWidth: 1200, fitWidth: 832 });
    expect(drag).toMatchObject({ width: 840, atMax: true, atFit: false });
  });

  it("has no fit guide when the window is too narrow to fit every project", () => {
    const drag = resolveChatListDrag({ wanted: 835, containerWidth: 1200, fitWidth: 840 });
    expect(drag).toEqual({ width: 835, atMax: false, atFit: false, fitGuide: null });
  });
});

describe("toggleChatListWidth", () => {
  it("expands to the fit width, then goes back to the default", () => {
    expect(toggleChatListWidth({ width: 300, containerWidth: 1200, fitWidth: 500 })).toEqual({
      width: 500,
      reason: "fit",
    });
    expect(toggleChatListWidth({ width: 500, containerWidth: 1200, fitWidth: 500 })).toEqual({
      width: 300,
      reason: "default",
    });
  });

  it("goes as wide as the window allows when the fit width is out of reach", () => {
    expect(toggleChatListWidth({ width: 300, containerWidth: 800, fitWidth: 500 })).toEqual({
      width: 440,
      reason: "max",
    });
    expect(toggleChatListWidth({ width: 440, containerWidth: 800, fitWidth: 500 })).toEqual({
      width: 300,
      reason: "default",
    });
  });

  it("returns to the default when everything already fits or nothing is measured", () => {
    expect(toggleChatListWidth({ width: 420, containerWidth: 1200, fitWidth: 280 })).toEqual({
      width: 300,
      reason: "default",
    });
    expect(toggleChatListWidth({ width: 420, containerWidth: 1200, fitWidth: null })).toEqual({
      width: 300,
      reason: "default",
    });
  });
});
