/**
 * The chat's progress bar (DENE-1667), above the composer: how many issues
 * this chat opened, how many moved since the last look, and who they wait
 * on. A tap opens the `chat-progress` formSheet with the list. Mirrors web's
 * `ChatReportBar` (packages/views/chat/components/chat-report-bar.tsx).
 *
 * 听汇报 shows only when the chat carries a project (a report is per project;
 * the phone cannot pick one, but a chat opened elsewhere may have it) and
 * sends the same prompt as web. The sheet is a native formSheet, so the
 * grabber swipe and the backdrop tap close it.
 */
import { Pressable, View } from "react-native";
import { router } from "expo-router";
import { Ionicons } from "@expo/vector-icons";
import type { ChatTicket } from "@multica/core/types";
import { Text } from "@/components/ui/text";
import { chatProgress } from "@/lib/chat-tickets";
import { useChatProgressSeenAt } from "@/data/stores/chat-progress-seen-store";
import { useWorkspaceStore } from "@/data/workspace-store";
import { cn } from "@/lib/utils";
import { useColorScheme } from "@/lib/use-color-scheme";
import { THEME } from "@/lib/theme";
import { useT } from "@/lib/i18n";

export function ChatProgressBar({
  sessionId,
  tickets,
  projectTitle,
  hearDisabled,
  onHear,
}: {
  sessionId: string | null;
  tickets: ChatTicket[] | undefined;
  /** The chat's projects, named; empty hides 听汇报. */
  projectTitle: string;
  hearDisabled: boolean;
  onHear: (prompt: string) => void;
}) {
  const { t } = useT("chat");
  const wsSlug = useWorkspaceStore((s) => s.currentWorkspaceSlug);
  const seenAt = useChatProgressSeenAt(sessionId);
  const { colorScheme } = useColorScheme();
  if (!sessionId || !tickets || tickets.length === 0) return null;
  const { rows, counts, fresh } = chatProgress(tickets, seenAt);
  const parts = [
    counts.waiting_you > 0 && t("progress.waiting_you", { count: counts.waiting_you }),
    counts.in_progress > 0 && t("progress.in_progress", { count: counts.in_progress }),
    counts.done > 0 && t("progress.done", { count: counts.done }),
  ].filter(Boolean);

  return (
    <View className="flex-row items-center pr-2">
    <Pressable
      accessibilityRole="button"
      onPress={() => {
        if (!wsSlug) return;
        router.push({
          pathname: "/[workspace]/chat-progress",
          params: { workspace: wsSlug, session: sessionId },
        });
      }}
      className="min-h-11 flex-1 flex-row items-center gap-1.5 px-4 active:opacity-70"
    >
      <View
        className={cn("size-1.5 rounded-full", fresh > 0 ? "bg-brand" : "bg-muted-foreground/40")}
      />
      <Text className="shrink text-xs text-foreground" numberOfLines={1}>
        {fresh > 0
          ? t("progress.bar_fresh", { count: rows.length, fresh })
          : t("progress.bar", { count: rows.length })}
        {parts.length > 0 && (
          <Text className="text-xs text-muted-foreground">{` · ${parts.join(" · ")}`}</Text>
        )}
      </Text>
      <Ionicons name="chevron-up" size={14} color={THEME[colorScheme].mutedForeground} />
    </Pressable>
    {projectTitle ? (
      <Pressable
        accessibilityRole="button"
        disabled={hearDisabled}
        onPress={() => onHear(t("progress.hear_prompt", { project: projectTitle }))}
        className={cn(
          "min-h-11 justify-center px-3 active:opacity-70",
          hearDisabled && "opacity-40",
        )}
      >
        <Text className="text-xs font-medium text-brand">{t("progress.hear")}</Text>
      </Pressable>
    ) : null}
    </View>
  );
}
