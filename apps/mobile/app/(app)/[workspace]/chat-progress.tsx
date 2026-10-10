/**
 * The chat's progress sheet (DENE-1667) — presented as a formSheet by the
 * parent Stack from `ChatProgressBar`. Lists the issues this chat opened or
 * follows (each row leads with where it came from),
 * fresh moves first (「刚变：A → B · 多久前」); a row opens the issue.
 * Closing the sheet is the "look": the next opening marks only later moves.
 */
import { useEffect } from "react";
import { InteractionManager, Pressable, ScrollView, View } from "react-native";
import { router, useLocalSearchParams } from "expo-router";
import { useQuery } from "@tanstack/react-query";
import { Text } from "@/components/ui/text";
import { StatusIcon } from "@/components/ui/status-icon";
import { chatTicketsOptions } from "@/data/queries/chat";
import { useWorkspaceStore } from "@/data/workspace-store";
import {
  useChatProgressSeenAt,
  useChatProgressSeenStore,
} from "@/data/stores/chat-progress-seen-store";
import { chatProgress } from "@/lib/chat-tickets";
import { useIssueStatuses } from "@/lib/use-issue-statuses";
import { timeAgo } from "@/lib/time-ago";
import { useT } from "@/lib/i18n";

export default function ChatProgressRoute() {
  const { session } = useLocalSearchParams<{ session: string }>();
  const sessionId = session ?? null;
  const wsId = useWorkspaceStore((s) => s.currentWorkspaceId);
  const wsSlug = useWorkspaceStore((s) => s.currentWorkspaceSlug);
  const { t } = useT("chat");
  const catalog = useIssueStatuses();
  const { data } = useQuery(chatTicketsOptions(wsId, sessionId));
  const seenAt = useChatProgressSeenAt(sessionId);
  const markSeen = useChatProgressSeenStore((s) => s.markSeen);
  useEffect(() => () => {
    if (sessionId) markSeen(sessionId);
  }, [sessionId, markSeen]);

  const { rows } = chatProgress(data?.tickets ?? [], seenAt);

  return (
    <View className="flex-1">
      <View className="px-4 pt-4 pb-3">
        <Text className="text-base font-semibold text-foreground">
          {t("progress.title")}
        </Text>
      </View>
      <ScrollView className="flex-1" showsVerticalScrollIndicator={false}>
        {rows.map((row) => {
          const where = row.from_status
            ? `${catalog.labelOf(row.from_status)} → ${catalog.labelOf(row.status)}`
            : catalog.labelOf(row.status);
          const meta = [
            t(`tickets.source.${row.source}`),
            row.fresh ? `${t("progress.just_changed")}：${where}` : where,
            row.changed_at ? timeAgo(row.changed_at) : "",
            row.needs_you ? t("progress.needs_you") : "",
          ]
            .filter(Boolean)
            .join(" · ");
          return (
            <Pressable
              key={row.id}
              accessibilityRole="link"
              onPress={() => {
                if (!wsSlug) return;
                router.back();
                // Push once the sheet's dismiss animation settles, as
                // project/new.tsx does.
                InteractionManager.runAfterInteractions(() => {
                  router.push(`/${wsSlug}/issue/${row.id}`);
                });
              }}
              className="min-h-11 flex-row items-center gap-2 px-4 py-2 active:opacity-70"
            >
              <StatusIcon
                status={row.status}
                category={catalog.categoryOf(row.status)}
                icon={catalog.iconOf(row.status)}
                color={catalog.colorOf(row.status)}
                size={14}
              />
              <View className="min-w-0 flex-1">
                <View className="min-w-0 flex-row items-baseline gap-1.5">
                  <Text className="shrink-0 text-sm text-muted-foreground">{row.identifier}</Text>
                  <Text className="shrink text-sm text-foreground" numberOfLines={1}>
                    {row.title}
                  </Text>
                </View>
                <Text
                  className={row.fresh ? "text-xs text-foreground" : "text-xs text-muted-foreground"}
                  numberOfLines={2}
                >
                  {meta}
                </Text>
              </View>
            </Pressable>
          );
        })}
      </ScrollView>
    </View>
  );
}
