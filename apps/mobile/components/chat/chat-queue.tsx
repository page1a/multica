/**
 * Follow-ups waiting behind the running reply (DENE-1362), mirroring Web's
 * `packages/views/chat/components/chat-queue.tsx`. Queued messages are
 * hidden from the transcript (`hideQueuedChatMessages`), so without this list
 * a message sent mid-reply would vanish until its turn.
 *
 * Each row: a steered message is tagged「插话中」; 打断重来 moves it to the
 * front and stops the current reply; the trash icon drops it. Web's edit and
 * clear-all live in its ⋯ menu and are left out here — remove + resend covers
 * edit on a phone.
 */
import { useState } from "react";
import { ActivityIndicator, Pressable, ScrollView, View } from "react-native";
import { Ionicons } from "@expo/vector-icons";
import type { ChatQueuedTask } from "@multica/core/types";
import { Text } from "@/components/ui/text";
import { IconButton } from "@/components/ui/icon-button";
import { useColorScheme } from "@/lib/use-color-scheme";
import { THEME } from "@/lib/theme";
import { cn } from "@/lib/utils";
import { useT } from "@/lib/i18n";

interface Props {
  tasks: ChatQueuedTask[];
  headStatus: string | undefined;
  /** The caller can no longer run this agent; restarting would be refused. */
  sendNowDisabled?: boolean;
  onSendNow: (taskId: string) => Promise<void>;
  onRemove: (taskId: string) => Promise<void>;
}

export function ChatQueue({
  tasks,
  headStatus,
  sendNowDisabled = false,
  onSendNow,
  onRemove,
}: Props) {
  const { t } = useT("chat");
  const { colorScheme } = useColorScheme();
  const theme = THEME[colorScheme];
  const [busy, setBusy] = useState<string | null>(null);

  if (tasks.length === 0) return null;

  const dispatchableHead =
    headStatus === "dispatched" ||
    headStatus === "running" ||
    headStatus === "waiting_local_directory";
  const canSendNow = !sendNowDisabled && dispatchableHead;
  const sendNowLabel = canSendNow
    ? t("queue.steer")
    : sendNowDisabled
      ? t("queue.steer_no_permission")
      : t("queue.steer_unavailable");

  const run = async (key: string, action: () => Promise<void>) => {
    setBusy(key);
    try {
      await action();
    } finally {
      setBusy((current) => (current === key ? null : current));
    }
  };

  return (
    <View className="border-t border-border bg-background px-3 pt-1">
      <ScrollView style={{ maxHeight: 132 }} keyboardShouldPersistTaps="handled">
        {tasks.map((task) => {
          const sendKey = `send:${task.task_id}`;
          const removeKey = `remove:${task.task_id}`;
          return (
            <View
              key={task.task_id}
              className="min-h-11 flex-row items-center gap-2"
            >
              <Ionicons
                name="return-down-forward-outline"
                size={14}
                color={theme.mutedForeground}
              />
              <Text
                className="flex-1 text-sm text-muted-foreground"
                numberOfLines={1}
              >
                {task.steering ? (
                  <Text className="text-sm text-foreground">
                    {t("queue.steering")} ·{" "}
                  </Text>
                ) : null}
                {task.content?.trim() || t("queue.fallback")}
              </Text>
              <Pressable
                onPress={() => void run(sendKey, () => onSendNow(task.task_id))}
                disabled={busy !== null || !canSendNow}
                hitSlop={8}
                accessibilityRole="button"
                accessibilityLabel={sendNowLabel}
                accessibilityState={{ disabled: busy !== null || !canSendNow }}
                className={cn(
                  "h-8 justify-center rounded-full px-2.5 active:bg-secondary",
                  (busy !== null || !canSendNow) && "opacity-50",
                )}
              >
                {busy === sendKey ? (
                  <ActivityIndicator size="small" color={theme.mutedForeground} />
                ) : (
                  <Text className="text-sm text-foreground">{t("queue.steer")}</Text>
                )}
              </Pressable>
              {busy === removeKey ? (
                <View className="h-8 w-8 items-center justify-center">
                  <ActivityIndicator size="small" color={theme.mutedForeground} />
                </View>
              ) : (
                <IconButton
                  name="trash-outline"
                  iconSize={16}
                  color={theme.mutedForeground}
                  onPress={() => void run(removeKey, () => onRemove(task.task_id))}
                  disabled={busy !== null}
                  accessibilityLabel={t("queue.remove")}
                  className="h-8 w-8"
                />
              )}
            </View>
          );
        })}
      </ScrollView>
    </View>
  );
}
