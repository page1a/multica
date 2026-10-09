/**
 * "From chat" line inside `IssueHeaderCard` (DENE-1665).
 *
 * Mirrors the `source_chat` line in web's issue detail
 * (packages/views/issues/components/issue-detail.tsx): an accessible chat is
 * tappable and opens that session in the Chat tab; one the viewer cannot see
 * renders as plain muted text with no title.
 *
 * Opening a specific session goes through the chat-session picker store's
 * one-shot `requestSelect` — the same channel the session sheet uses — then
 * navigates to the Chat tab, which applies it.
 */
import { Pressable, View } from "react-native";
import { router } from "expo-router";
import { Ionicons } from "@expo/vector-icons";
import type { Issue } from "@multica/core/types";
import { Text } from "@/components/ui/text";
import { useWorkspaceStore } from "@/data/workspace-store";
import { useChatSessionPickerStore } from "@/data/stores/chat-session-picker-store";
import { useColorScheme } from "@/lib/use-color-scheme";
import { THEME } from "@/lib/theme";
import { useT } from "@/lib/i18n";

export function SourceChatRow({ issue }: { issue: Issue }) {
  const { t } = useT("issues");
  const wsSlug = useWorkspaceStore((s) => s.currentWorkspaceSlug);
  const { colorScheme } = useColorScheme();
  const source = issue.source_chat;
  if (!source) return null;
  const icon = (
    <Ionicons
      name="chatbubbles-outline"
      size={14}
      color={THEME[colorScheme].mutedForeground}
    />
  );

  if (!source.accessible) {
    return (
      <View className="flex-row items-center gap-1.5">
        {icon}
        <Text className="text-sm text-muted-foreground">
          {t("detail.from_chat_hidden")}
        </Text>
      </View>
    );
  }

  return (
    <Pressable
      accessibilityRole="link"
      onPress={() => {
        if (!wsSlug) return;
        useChatSessionPickerStore.getState().requestSelect(source.id);
        router.navigate(`/${wsSlug}/chat`);
      }}
      className="min-h-11 flex-row items-center gap-1.5 active:opacity-70"
    >
      {icon}
      <Text className="shrink-0 text-sm font-medium text-muted-foreground">
        {t("detail.from_chat")}
      </Text>
      {source.title ? (
        <Text className="shrink text-sm text-muted-foreground" numberOfLines={1}>
          {source.title}
        </Text>
      ) : null}
    </Pressable>
  );
}
