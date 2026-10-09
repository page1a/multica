/**
 * State card on the issue detail screen (DENE-1328).
 *
 * Mirrors `packages/views/issues/components/issue-state-card.tsx`: the server
 * derives the card (`GET /api/issues/:id/context`, the same one
 * `multica issue context` prints). Goal and "where it stands" are already in
 * the header (title, status chip), so only three rows render here:
 *
 *   Settled              · decision one          (tap → Edit / Delete)
 *                        + Add a decision        (Alert.prompt)
 *   Last baton           summary · Handoff to X · 2h ago
 *   Since you were here  thread title   2 new
 *   Sub-task results     ● DENE-2 title · conclusion · #5 merged  (tap → issue)
 *
 * Edits go through `/api/issues/:id/decisions` with the same server rules
 * as web and CLI; a refusal is shown as an alert with the server's reason.
 */
import { useCallback } from "react";
import { ActionSheetIOS, Alert, Pressable, View } from "react-native";
import { router } from "expo-router";
import { useQuery } from "@tanstack/react-query";
import { Ionicons } from "@expo/vector-icons";
import type { StateCardChildReceipt, StateCardDecision } from "@multica/core/types";
import { Text } from "@/components/ui/text";
import { issueContextOptions } from "@/data/queries/issues";
import { useIssueDecisionMutations } from "@/data/mutations/issues";
import { useWorkspaceStore } from "@/data/workspace-store";
import { useColorScheme } from "@/lib/use-color-scheme";
import { THEME } from "@/lib/theme";
import { useT, useTimeAgo } from "@/lib/i18n";

export function StateCard({ issueId }: { issueId: string }) {
  const { t } = useT("issues");
  const timeAgo = useTimeAgo();
  const wsId = useWorkspaceStore((s) => s.currentWorkspaceId);
  const { data: card } = useQuery(issueContextOptions(wsId, issueId));
  if (!card) return null;

  const baton = card.baton;
  const changes = card.changes;

  return (
    <View className="gap-3" testID="issue-state-card">
      <Decisions issueId={issueId} decisions={card.decisions} />

      <Row label={t("state_card.baton")}>
        {baton?.summary ? (
          <>
            <Text className="text-sm text-foreground">{baton.summary}</Text>
            <Text className="text-xs text-muted-foreground">
              {[
                baton.kind === "handoff"
                  ? t("state_card.baton_handoff", { to: baton.to ?? "" })
                  : t("state_card.baton_close"),
                baton.at ? timeAgo(baton.at) : null,
              ]
                .filter(Boolean)
                .join(" · ")}
            </Text>
          </>
        ) : (
          <Text className="text-sm text-muted-foreground">{t("state_card.baton_none")}</Text>
        )}
      </Row>

      {card.children && card.children.length > 0 ? <ChildReceipts receipts={card.children} /> : null}

      <Row
        label={
          changes.anchor === "none" ? t("state_card.changes_first") : t("state_card.changes")
        }
      >
        {changes.threads.length === 0 ? (
          <Text className="text-sm text-muted-foreground">{t("state_card.changes_none")}</Text>
        ) : (
          <>
            {changes.threads.map((thread) => (
              <View key={thread.thread_id} className="flex-row items-baseline gap-2">
                <Text className="flex-1 text-sm text-foreground" numberOfLines={1}>
                  {thread.title}
                </Text>
                <Text className="text-xs text-muted-foreground">
                  {t("state_card.changes_count", { count: thread.new_count })}
                </Text>
              </View>
            ))}
            {changes.more ? (
              <Text className="text-xs text-muted-foreground">
                {t("state_card.changes_more", { count: changes.more })}
              </Text>
            ) : null}
          </>
        )}
      </Row>
    </View>
  );
}

function ChildReceipts({ receipts }: { receipts: StateCardChildReceipt[] }) {
  const { t } = useT("issues");
  const wsSlug = useWorkspaceStore((s) => s.currentWorkspaceSlug);
  return (
    <Row label={t("state_card.children")}>
      {receipts.map((r) => {
        const prs = r.pull_requests
          .map((pr) =>
            [pr.number > 0 ? `#${pr.number}` : "PR", pr.state === "merged" ? t("state_card.children_pr_merged") : null]
              .filter(Boolean)
              .join(" "),
          )
          .join("，");
        const meta = [prs, r.knowledge].filter(Boolean).join(" · ");
        return (
          <Pressable
            key={r.issue_id}
            accessibilityRole="link"
            onPress={() => {
              if (wsSlug) router.push(`/${wsSlug}/issue/${r.issue_id}`);
            }}
            className="min-h-11 justify-center gap-0.5 py-1 active:opacity-60"
          >
            <Text className="text-sm text-foreground" numberOfLines={1}>
              {r.identifier} {r.title}
            </Text>
            {r.summary ? (
              <Text className="text-sm text-foreground" numberOfLines={2}>
                {r.summary}
              </Text>
            ) : null}
            {meta ? (
              <Text className="text-xs text-muted-foreground" numberOfLines={1}>
                {meta}
              </Text>
            ) : null}
          </Pressable>
        );
      })}
    </Row>
  );
}

function Row({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <View className="gap-1">
      <Text className="text-xs text-muted-foreground">{label}</Text>
      {children}
    </View>
  );
}

function Decisions({
  issueId,
  decisions,
}: {
  issueId: string;
  decisions: StateCardDecision[];
}) {
  const { t } = useT("issues");
  const { colorScheme } = useColorScheme();
  const mutedFg = THEME[colorScheme].mutedForeground;
  const { add, edit, remove } = useIssueDecisionMutations(issueId);

  const onError = useCallback(
    (error: unknown) =>
      Alert.alert(
        t("state_card.decision_failed", {
          reason: error instanceof Error ? error.message : "",
        }),
      ),
    [t],
  );

  const onAdd = useCallback(() => {
    Alert.prompt(t("state_card.decision_add"), t("state_card.decision_placeholder"), (value) => {
      const text = value?.trim();
      if (text) add.mutate(text, { onError });
    });
  }, [add, onError, t]);

  const onDecision = useCallback(
    (d: StateCardDecision) => {
      ActionSheetIOS.showActionSheetWithOptions(
        {
          options: [
            t("common:actions.cancel"),
            t("state_card.decision_edit"),
            t("state_card.decision_delete"),
          ],
          cancelButtonIndex: 0,
          destructiveButtonIndex: 2,
          title: d.text,
        },
        (i) => {
          if (i === 1)
            Alert.prompt(
              t("state_card.decision_edit"),
              undefined,
              (value) => {
                const text = value?.trim();
                if (text && text !== d.text) edit.mutate({ id: d.id, text }, { onError });
              },
              "plain-text",
              d.text,
            );
          else if (i === 2) remove.mutate(d.id, { onError });
        },
      );
    },
    [edit, onError, remove, t],
  );

  return (
    <Row label={t("state_card.decisions")}>
      {decisions.length === 0 ? (
        <Text className="text-sm text-muted-foreground">{t("state_card.decisions_empty")}</Text>
      ) : (
        decisions.map((d) => (
          <Pressable
            key={d.id}
            onPress={() => onDecision(d)}
            accessibilityRole="button"
            accessibilityHint={`${t("state_card.decision_edit")} · ${t("state_card.decision_delete")}`}
            className="min-h-11 flex-row items-center active:opacity-60"
          >
            <Text className="flex-1 text-sm text-foreground">{d.text}</Text>
          </Pressable>
        ))
      )}
      <Pressable
        onPress={onAdd}
        disabled={add.isPending}
        accessibilityRole="button"
        className="min-h-11 flex-row items-center gap-1 self-start active:opacity-60"
      >
        <Ionicons name="add" size={16} color={mutedFg} />
        <Text className="text-sm text-muted-foreground">{t("state_card.decision_add")}</Text>
      </Pressable>
    </Row>
  );
}
