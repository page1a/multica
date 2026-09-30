import { afterEach, describe, expect, it } from "vitest";
import { chatPageShortcutAction } from "./chat-page-shortcuts";
import { createShortcutChord, configureShortcutRuntime, useShortcutStore } from "../shortcuts";

function keyEvent(
  key: string,
  fields: Partial<Pick<KeyboardEvent, "metaKey" | "ctrlKey" | "altKey" | "shiftKey">> = {},
): KeyboardEvent {
  return {
    key,
    metaKey: false,
    ctrlKey: false,
    altKey: false,
    shiftKey: false,
    ...fields,
  } as KeyboardEvent;
}

const open = { inForeignEditable: false, inPortal: false };

afterEach(() => {
  useShortcutStore.getState().resetAll();
  configureShortcutRuntime(null);
});

describe("chatPageShortcutAction", () => {
  it("starts a chat on the runtime default and ignores the other runtime's chord", () => {
    configureShortcutRuntime("desktop");
    const macN = keyEvent("n", { metaKey: true });
    expect(chatPageShortcutAction(macN, open, "macos")).toBe("new-chat");
    const winN = keyEvent("n", { ctrlKey: true });
    expect(chatPageShortcutAction(winN, open, "windows")).toBe("new-chat");

    configureShortcutRuntime("web");
    expect(chatPageShortcutAction(macN, open, "macos")).toBeNull();
    expect(chatPageShortcutAction(winN, open, "windows")).toBeNull();
    const macE = keyEvent("e", { metaKey: true, shiftKey: true });
    const winE = keyEvent("e", { ctrlKey: true, shiftKey: true });
    expect(chatPageShortcutAction(macE, open, "macos")).toBe("new-chat");
    expect(chatPageShortcutAction(winE, open, "windows")).toBe("new-chat");
  });

  it("follows a saved binding instead of the runtime default", () => {
    configureShortcutRuntime("web");
    useShortcutStore.getState().setShortcut(
      "newChat",
      createShortcutChord("G", { primary: true }),
    );
    expect(chatPageShortcutAction(keyEvent("g", { metaKey: true }), open, "macos")).toBe("new-chat");
    expect(
      chatPageShortcutAction(keyEvent("e", { metaKey: true, shiftKey: true }), open, "macos"),
    ).toBeNull();

    useShortcutStore.getState().setShortcut("newChat", null);
    expect(chatPageShortcutAction(keyEvent("g", { metaKey: true }), open, "macos")).toBeNull();
  });

  it("opens the project switcher on Mod+\\ unless the person rebound it", () => {
    configureShortcutRuntime("desktop");
    const mac = keyEvent("\\", { metaKey: true });
    const win = keyEvent("\\", { ctrlKey: true });
    expect(chatPageShortcutAction(mac, open, "macos")).toBe("switch-project");
    expect(chatPageShortcutAction(win, open, "windows")).toBe("switch-project");
    // The old hardcoded chord is no longer the binding.
    expect(
      chatPageShortcutAction(keyEvent("p", { metaKey: true, altKey: true }), open, "macos"),
    ).toBeNull();

    useShortcutStore.getState().setShortcut(
      "switchChatProject",
      createShortcutChord("G", { primary: true, shift: true }),
    );
    expect(
      chatPageShortcutAction(keyEvent("g", { metaKey: true, shiftKey: true }), open, "macos"),
    ).toBe("switch-project");
    expect(chatPageShortcutAction(mac, open, "macos")).toBeNull();
  });

  it("stays quiet inside a popup or a text field that is not the composer", () => {
    configureShortcutRuntime("desktop");
    const macN = keyEvent("n", { metaKey: true });
    expect(
      chatPageShortcutAction(macN, { inForeignEditable: true, inPortal: false }, "macos"),
    ).toBeNull();
    expect(
      chatPageShortcutAction(macN, { inForeignEditable: false, inPortal: true }, "macos"),
    ).toBeNull();
  });
});
