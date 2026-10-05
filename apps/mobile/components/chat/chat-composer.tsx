/**
 * Chat composer — thin wrapper around the shared `<MessageComposer>` with
 * chat-specific wiring:
 *
 *   - **Controlled text**: parent (chat.tsx) owns the draft via
 *     `useChatDraftsStore` so switching sessions rehydrates the right
 *     draft. Pass `value` + `onChangeText` through.
 *   - **Stop button**: while an agent task is running for the active
 *     session, `sending` flips true and we replace the Send button slot
 *     with a Stop affordance (filled foreground bg + stop glyph). Tap →
 *     `onStop()` cancels the in-flight task.
 *   - **Send while running** (DENE-1362, Web parity with DENE-1346): when
 *     the server takes follow-ups, Stop stays and a split「插话 ▾」button
 *     sits beside it. The main half sends in the current mode (steer when
 *     the CLI supports it, else queue); ▾ opens a bottom sheet with
 *     steer / queue / restart, each with its process line, steer disabled
 *     with the reason when the CLI can't take it. The choice lasts for
 *     this reply only.
 *   - **Mention picker mode=chat**: chat is user ↔ single agent so
 *     @member / @agent / @squad / @all are noise + would notify the
 *     wrong people. Picker route honors `?mode=chat` and surfaces only
 *     Issues (useful for "reference this ticket for context").
 *   - **No reply target**: chat is a flat conversation; passes no
 *     reply chip.
 *   - **No upload context**: chat attachments are session-scoped; the
 *     server back-fills `chat_message_id` on each row when the message
 *     persists (server-side). `MessageComposer` calls `api.uploadFile`
 *     without `{ issueId, commentId }`.
 *   - **Parent owns keyboard**: chat.tsx wraps in KeyboardAvoidingView +
 *     SafeAreaView, so `manageKeyboard={false}` prevents the composer
 *     from double-stacking its own keyboard handling.
 *
 * Previously a hand-written 400-LOC twin of inline-comment-composer.tsx;
 * now ~50 LOC plus the StopButton subcomponent.
 */
import { useCallback, useEffect, useRef, useState } from "react";
import { ActivityIndicator, Modal, Pressable, View } from "react-native";
import { useSafeAreaInsets } from "react-native-safe-area-context";
import { Ionicons } from "@expo/vector-icons";
import type { ChatSendMode } from "@multica/core/types";
import Animated, { FadeIn, FadeOut } from "react-native-reanimated";
import * as Haptics from "expo-haptics";
import { MessageComposer } from "@/components/composer/message-composer";
import { useWorkspaceStore } from "@/data/workspace-store";
import { useColorScheme } from "@/lib/use-color-scheme";
import { THEME } from "@/lib/theme";
import { useT } from "@/lib/i18n";
import { Text } from "@/components/ui/text";
import { cn } from "@/lib/utils";
import {
  CHAT_SEND_MODES,
  effectiveChatSendMode,
  steerCliName,
} from "@/lib/chat-send-mode";

interface Props {
  /** Current draft text (controlled). Empty string = no draft. */
  value: string;
  /** Fired on every keystroke. The caller writes to the drafts store. */
  onChangeText: (next: string) => void;
  /** Send the serialised markdown content + the completed attachments'
   *  server ids. Caller resets the input by setting `value=""` after a
   *  successful send. */
  onSend: (
    content: string,
    attachmentIds: string[],
    mode?: ChatSendMode,
  ) => Promise<void> | void;
  /** Cancel the in-flight agent task. Only callable while `sending===true`. */
  onStop: () => void;
  /** True while an agent task is running for the active session. The
   *  composer swaps Send for Stop. */
  sending: boolean;
  /** Queued tasks remain busy, but do not expose Stop without draft restore. */
  allowStop?: boolean;
  /** The server takes follow-ups, so the reply in progress can get a
   *  message (steer / queue / restart) instead of only Stop. */
  allowSendWhileRunning?: boolean;
  /** The running reply can read a message mid-reply, so sends default to steer. */
  steerSupported?: boolean;
  /** CLI of the running reply, named when steer is unavailable. */
  steerProvider?: string;
  /** "restart" when steering stops the CLI and resumes its session. */
  steerMode?: string;
  /** Hard-disable typing + send. Used when there's no usable agent in the
   *  workspace or the session is archived (legacy). */
  disabled?: boolean;
  /** When `disabled`, replaces the pill label with the reason. */
  disabledReason?: string;
}

const IS_IOS = process.env.EXPO_OS === "ios";

export function ChatComposer({
  value,
  onChangeText,
  onSend,
  onStop,
  sending,
  allowStop = true,
  allowSendWhileRunning = false,
  steerSupported,
  steerProvider,
  steerMode,
  disabled = false,
  disabledReason,
}: Props) {
  const wsSlug = useWorkspaceStore((s) => s.currentWorkspaceSlug);
  const { t } = useT("chat");

  const onSubmit = useCallback(
    async ({
      content,
      attachmentIds,
      mode,
    }: {
      content: string;
      attachmentIds: string[];
      mode?: ChatSendMode;
    }) => {
      // `onSend` may be sync or async; await is safe in both cases. If it
      // throws, MessageComposer's catch restores text + chips.
      await onSend(content, attachmentIds, mode);
    },
    [onSend],
  );

  // The mode for this reply only; a new reply starts from the default again.
  const [chosenMode, setChosenMode] = useState<ChatSendMode | null>(null);
  const [sheetOpen, setSheetOpen] = useState(false);
  const sendMode = effectiveChatSendMode(chosenMode, steerSupported);
  const sendModeActive = sending && allowSendWhileRunning && !disabled;
  // Latest send handle from the composer's running slot. The sheet lives
  // outside the composer so it survives the composer collapsing to its pill
  // when an empty input blurs as the modal opens.
  const runningSendRef = useRef<{
    canSend: boolean;
    submit: (mode: ChatSendMode) => void;
  } | null>(null);
  useEffect(() => {
    if (!sending) {
      setChosenMode(null);
      setSheetOpen(false);
    }
  }, [sending]);

  const handleStop = useCallback(() => {
    if (IS_IOS) {
      void Haptics.impactAsync(Haptics.ImpactFeedbackStyle.Medium);
    }
    onStop();
  }, [onStop]);

  return (
    <>
    <MessageComposer
      value={value}
      onChangeText={onChangeText}
      onSubmit={onSubmit}
      mentionPickerPath={{
        pathname: "/[workspace]/mention-picker",
        params: { workspace: wsSlug ?? "", mode: "chat" },
      }}
      placeholder={sending ? t("composer.working") : t("composer.message")}
      pillLabel={
        sending
          ? t("composer.working")
          : disabled
            ? (disabledReason ?? t("composer.unavailable"))
            : t("composer.message")
      }
      pillIcon="chatbubble-ellipses-outline"
      disabled={disabled}
      disabledReason={disabledReason}
      isSending={sending}
      renderStop={allowStop ? () => <StopButton onPress={handleStop} /> : undefined}
      renderRunning={
        sendModeActive
          ? ({ canSend, submitting, submit }) => {
              runningSendRef.current = { canSend, submit };
              return (
              <View className="flex-row items-center gap-2">
                {allowStop ? <StopButton onPress={handleStop} /> : null}
                <SendModeButton
                  mode={sendMode}
                  canSend={canSend}
                  submitting={submitting}
                  onSend={() => submit(sendMode)}
                  onOpenMenu={() => setSheetOpen(true)}
                />
              </View>
              );
            }
          : undefined
      }
      manageKeyboard={false}
    />
    <SendModeSheet
      visible={sheetOpen && sendModeActive}
      mode={sendMode}
      steerSupported={steerSupported === true}
      steerProvider={steerProvider}
      steerMode={steerMode}
      onClose={() => setSheetOpen(false)}
      onPick={(next) => {
        setChosenMode(next);
        setSheetOpen(false);
        // A draft goes out right away in the picked mode; an empty draft
        // only sets the mode, same as Web.
        const running = runningSendRef.current;
        if (running?.canSend) running.submit(next);
      }}
    />
    </>
  );
}

function modeLabelKey(mode: ChatSendMode) {
  return mode === "steer"
    ? "composer.mode_steer"
    : mode === "queue"
      ? "composer.mode_queue"
      : "composer.mode_restart";
}

/** Split send button: the label half sends in `mode`, ▾ opens the sheet. */
function SendModeButton({
  mode,
  canSend,
  submitting,
  onSend,
  onOpenMenu,
}: {
  mode: ChatSendMode;
  canSend: boolean;
  submitting: boolean;
  onSend: () => void;
  onOpenMenu: () => void;
}) {
  const { colorScheme } = useColorScheme();
  const theme = THEME[colorScheme];
  const { t } = useT("chat");
  const label = t(modeLabelKey(mode));
  return (
    <View className="h-8 flex-row items-center overflow-hidden rounded-full bg-primary">
      <Pressable
        onPress={onSend}
        disabled={!canSend || submitting}
        hitSlop={{ top: 6, bottom: 6, left: 6 }}
        accessibilityRole="button"
        accessibilityLabel={label}
        accessibilityState={{ disabled: !canSend || submitting, busy: submitting }}
        className={cn(
          "h-8 justify-center pl-3 pr-2 active:opacity-80",
          (!canSend || submitting) && "opacity-50",
        )}
      >
        {submitting ? (
          <ActivityIndicator size="small" color={theme.primaryForeground} />
        ) : (
          <Text className="text-sm font-medium text-primary-foreground">{label}</Text>
        )}
      </Pressable>
      <View className="h-4 w-px bg-primary-foreground/30" />
      <Pressable
        onPress={onOpenMenu}
        hitSlop={{ top: 6, bottom: 6, right: 6 }}
        accessibilityRole="button"
        accessibilityLabel={t("composer.mode_menu")}
        className="h-8 justify-center pl-1.5 pr-2.5 active:opacity-80"
      >
        <Ionicons name="chevron-down" size={14} color={theme.primaryForeground} />
      </Pressable>
    </View>
  );
}

/** Bottom sheet listing the three modes, one 44pt+ row each. */
function SendModeSheet({
  visible,
  mode,
  steerSupported,
  steerProvider,
  steerMode,
  onClose,
  onPick,
}: {
  visible: boolean;
  mode: ChatSendMode;
  steerSupported: boolean;
  steerProvider?: string;
  steerMode?: string;
  onClose: () => void;
  onPick: (mode: ChatSendMode) => void;
}) {
  const { t } = useT("chat");
  const insets = useSafeAreaInsets();
  const descKey = (m: ChatSendMode) =>
    m === "steer"
      ? steerMode === "restart"
        ? "composer.mode_steer_restart_desc"
        : "composer.mode_steer_desc"
      : m === "queue"
        ? "composer.mode_queue_desc"
        : "composer.mode_restart_desc";
  const processKey = (m: ChatSendMode) =>
    m === "steer"
      ? steerMode === "restart"
        ? "composer.mode_steer_restart_process"
        : "composer.mode_steer_process"
      : m === "queue"
        ? "composer.mode_queue_process"
        : "composer.mode_restart_process";
  const steerReason = steerProvider
    ? t("composer.mode_steer_unsupported", { cli: steerCliName(steerProvider) })
    : t("composer.mode_steer_unsupported_generic");

  return (
    <Modal
      visible={visible}
      transparent
      animationType="slide"
      onRequestClose={onClose}
    >
      <Pressable
        className="flex-1 justify-end bg-black/40"
        onPress={onClose}
        accessibilityLabel={t("common:actions.cancel")}
      >
        <Pressable
          onPress={() => {}}
          className="rounded-t-2xl bg-popover px-2 pt-2"
          style={{ paddingBottom: Math.max(insets.bottom, 8) }}
        >
          <Text className="px-3 pb-1 pt-1 text-xs font-medium text-muted-foreground">
            {t("composer.mode_menu")}
          </Text>
          {CHAT_SEND_MODES.map((m) => {
            const disabled = m === "steer" && !steerSupported;
            const selected = m === mode;
            return (
              <Pressable
                key={m}
                disabled={disabled}
                onPress={() => onPick(m)}
                accessibilityRole="button"
                accessibilityState={{ disabled, selected }}
                className={cn(
                  "min-h-11 flex-row items-center gap-3 rounded-lg px-3 py-2 active:bg-secondary",
                  selected && "bg-secondary",
                  disabled && "opacity-60",
                )}
              >
                <View className="flex-1 gap-0.5">
                  <Text className="text-base text-foreground">
                    {t(modeLabelKey(m))}
                  </Text>
                  <Text className="text-xs text-muted-foreground">
                    {disabled ? steerReason : t(descKey(m))}
                  </Text>
                  {!disabled ? (
                    <Text className="text-xs text-muted-foreground">
                      {t(processKey(m))}
                    </Text>
                  ) : null}
                </View>
              </Pressable>
            );
          })}
        </Pressable>
      </Pressable>
    </Modal>
  );
}

function StopButton({ onPress }: { onPress: () => void }) {
  const { colorScheme } = useColorScheme();
  const { t } = useT("chat");
  const theme = THEME[colorScheme];
  return (
    <Animated.View
      key="stop"
      entering={FadeIn.duration(120)}
      exiting={FadeOut.duration(120)}
    >
      <Pressable
        onPress={onPress}
        className="h-8 w-8 items-center justify-center rounded-full bg-foreground active:opacity-80"
        hitSlop={12}
        accessibilityRole="button"
        accessibilityLabel={t("composer.stop_agent")}
      >
        <View
          style={{
            width: 10,
            height: 10,
            backgroundColor: theme.background,
            borderRadius: 1.5,
          }}
        />
      </Pressable>
    </Animated.View>
  );
}
