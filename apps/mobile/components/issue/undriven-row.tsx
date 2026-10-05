/**
 * "Nobody is moving this" row inside `IssueHeaderCard` (DENE-1342).
 *
 * Mirrors `packages/views/issues/components/issue-driver.tsx`: the server
 * computes `issue.driver` on the detail response, and only `kind: "none"`
 * renders. Tapping the row opens the four dispositions as a native action
 * sheet; split and cancel ask for their one word through `Alert.prompt`.
 * All four go through the same `POST /api/issues/:id/dispose` as web and CLI.
 *
 *   ● Nobody is moving this · reason · rerun 2×          Dispose ›
 *   driven / closed / backlog → null (zero space)
 */
import { useCallback } from "react";
import { ActionSheetIOS, Alert, Pressable, View } from "react-native";
import { Ionicons } from "@expo/vector-icons";
import type { Issue } from "@multica/core/types";
import { Text } from "@/components/ui/text";
import { useDisposeIssue } from "@/data/mutations/issues";
import { useColorScheme } from "@/lib/use-color-scheme";
import { THEME } from "@/lib/theme";
import { useT } from "@/lib/i18n";

export function UndrivenRow({ issue }: { issue: Issue }) {
  const { t } = useT("issues");
  const { colorScheme } = useColorScheme();
  const mutedFg = THEME[colorScheme].mutedForeground;
  const dispose = useDisposeIssue(issue.id);
  const driver = issue.driver;

  const send = useCallback(
    (body: Parameters<typeof dispose.mutate>[0]) =>
      dispose.mutate(body, {
        onError: (error) =>
          Alert.alert(error instanceof Error ? error.message : t("driver.failed")),
      }),
    [dispose, t],
  );

  const onPress = useCallback(() => {
    const actions = ["back", "rerun", "reroute", "split", "cancel"] as const;
    ActionSheetIOS.showActionSheetWithOptions(
      {
        options: actions.map((a) => (a === "back" ? t("common:actions.cancel") : t(`driver.${a}`))),
        cancelButtonIndex: 0,
        destructiveButtonIndex: actions.length - 1,
        title: issue.identifier,
      },
      (i) => {
        const action = actions[i];
        if (action === "rerun" || action === "reroute") send({ action });
        else if (action === "split")
          Alert.prompt(t("driver.split_prompt"), undefined, (value) => {
            if (value?.trim()) send({ action, into: [value.trim()] });
          });
        else if (action === "cancel")
          Alert.prompt(t("driver.cancel_prompt"), undefined, (value) => {
            if (value?.trim()) send({ action, reason: value.trim() });
          });
      },
    );
  }, [issue.identifier, send, t]);

  if (driver?.kind !== "none") return null;
  const detail = [
    driver.reason,
    driver.revives ? t("driver.revived", { count: driver.revives }) : null,
    driver.escalated ? t("driver.escalated") : null,
  ]
    .filter(Boolean)
    .join(" · ");

  return (
    <Pressable
      onPress={onPress}
      disabled={dispose.isPending}
      accessibilityRole="button"
      accessibilityLabel={`${t("driver.none")} · ${t("driver.actions")}`}
      className="min-h-11 flex-row items-center gap-2 active:opacity-60"
    >
      <View className="size-1.5 rounded-full bg-destructive" />
      <View className="flex-1">
        <Text className="text-sm font-medium text-destructive">{t("driver.none")}</Text>
        {detail ? (
          <Text className="text-xs text-muted-foreground" numberOfLines={2}>
            {detail}
          </Text>
        ) : null}
      </View>
      <Text className="text-sm text-muted-foreground">{t("driver.actions")}</Text>
      <Ionicons name="chevron-forward" size={16} color={mutedFg} />
    </Pressable>
  );
}
