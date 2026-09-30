import {
  getShortcut,
  getShortcutPlatform,
  shortcutMatchesEvent,
  type ShortcutPlatform,
} from "../shortcuts";

export type ChatPageShortcutAction = "new-chat" | "switch-project";

/**
 * Page-level chord, or null when the key should keep its ordinary meaning.
 * The chord is the person's saved binding, or the action's default for this
 * runtime. An open popup owns the keyboard (agent picker, switcher, menus).
 * A text field that is not the composer does too — search and rename must
 * not be hijacked. The composer itself is contenteditable and still starts
 * a chat.
 */
export function chatPageShortcutAction(
  event: KeyboardEvent,
  gates: { inForeignEditable: boolean; inPortal: boolean },
  platform: ShortcutPlatform = getShortcutPlatform(),
): ChatPageShortcutAction | null {
  if (gates.inPortal || gates.inForeignEditable) return null;
  if (shortcutMatchesEvent(getShortcut("newChat"), event, platform)) return "new-chat";
  if (shortcutMatchesEvent(getShortcut("switchChatProject"), event, platform)) return "switch-project";
  return null;
}
