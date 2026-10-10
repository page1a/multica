/**
 * The issues a chat turn opened or followed (DENE-1665, DENE-1719): where each
 * came from, who has it, why, and its live status, with the corrections a
 * reader makes without leaving the chat — including 取下, which takes the
 * issue off this chat without touching it (offered on settled rows too). Mirrors web's `ChatTicketCard`
 * (packages/views/chat/components/chat-ticket-card.tsx); issues are dispatched
 * as they are opened, so this card is where a wrong dispatch gets undone, not a
 * confirmation step.
 *
 * Phone adaptations:
 *   - Row actions open a native action sheet from a trailing "…" (44pt target)
 *     instead of web's dropdown.
 *   - No success toast (mobile has none); success is a haptic tick and the row
 *     updates from the refetch. Failure is a native alert.
 */
import { ActionSheetIOS, Alert, Pressable, View } from "react-native";
import { router } from "expo-router";
import * as Haptics from "expo-haptics";
import { useQueryClient } from "@tanstack/react-query";
import type { ChatTicket, UpdateIssueRequest } from "@multica/core/types";
import { Text } from "@/components/ui/text";
import { IconButton } from "@/components/ui/icon-button";
import { StatusIcon } from "@/components/ui/status-icon";
import { useAuthStore } from "@/data/auth-store";
import { useWorkspaceStore } from "@/data/workspace-store";
import { chatKeys } from "@/data/queries/chat";
import { useUpdateIssue } from "@/data/mutations/issues";
import { useUnpinChatTicket } from "@/data/mutations/chat";
import { useIssueStatuses } from "@/lib/use-issue-statuses";
import { useColorScheme } from "@/lib/use-color-scheme";
import { THEME } from "@/lib/theme";
import { useT } from "@/lib/i18n";

export function ChatTicketCard({
  sessionId,
  tickets,
}: {
  sessionId: string | null;
  tickets: ChatTicket[];
}) {
  const { t } = useT("chat");
  if (tickets.length === 0) return null;
  return (
    <View className="mt-2 rounded-md border border-border">
      <Text className="px-3 pt-2 pb-1 text-xs text-muted-foreground">
        {tickets.every((ticket) => ticket.source === "created")
          ? t("tickets.heading", { count: tickets.length })
          : t("tickets.heading_linked", { count: tickets.length })}
      </Text>
      <View className="pb-1">
        {tickets.map((ticket) => (
          <ChatTicketRow key={ticket.id} sessionId={sessionId} ticket={ticket} />
        ))}
      </View>
    </View>
  );
}

function ChatTicketRow({
  sessionId,
  ticket,
}: {
  sessionId: string | null;
  ticket: ChatTicket;
}) {
  const { t } = useT("chat");
  const qc = useQueryClient();
  const wsId = useWorkspaceStore((s) => s.currentWorkspaceId);
  const wsSlug = useWorkspaceStore((s) => s.currentWorkspaceSlug);
  const userId = useAuthStore((s) => s.user?.id ?? null);
  const catalog = useIssueStatuses();
  const update = useUpdateIssue(ticket.id);
  const unpin = useUnpinChatTicket(sessionId);
  const { colorScheme } = useColorScheme();

  const category = catalog.categoryOf(ticket.status);
  const settled = category === "done" || category === "closed";
  const unassigned = !ticket.assignee_type || !ticket.assignee_id;
  const mine =
    ticket.assignee_type === "member" && ticket.assignee_id === userId;
  const owner = unassigned
    ? t("tickets.routing")
    : t("tickets.to", { name: ticket.assignee_name || ticket.identifier });
  const detail = [
    t(`tickets.source.${ticket.source}`),
    catalog.labelOf(ticket.status),
    owner,
    ticket.goal,
  ]
    .filter(Boolean)
    .join(" · ");

  const run = (patch: UpdateIssueRequest) =>
    update.mutate(patch, {
      onSuccess: () => {
        void Haptics.notificationAsync(Haptics.NotificationFeedbackType.Success);
      },
      onError: () => {
        Alert.alert(t("tickets.failed", { id: ticket.identifier }));
      },
      onSettled: () => {
        void qc.invalidateQueries({ queryKey: chatKeys.ticketsAll(wsId) });
      },
    });

  const remove = () =>
    unpin.mutate(ticket.id, {
      onSuccess: () => {
        void Haptics.notificationAsync(Haptics.NotificationFeedbackType.Success);
      },
      onError: () => {
        Alert.alert(t("tickets.remove_failed", { id: ticket.identifier }));
      },
    });

  const openActions = () => {
    const actions: { label: string; destructive?: boolean; run: () => void }[] = [];
    // Settled issues need no correction; only 取下 applies to them.
    if (!settled && !mine && userId) {
      actions.push({
        label: t("tickets.assign_me"),
        run: () => run({ assignee_type: "member", assignee_id: userId }),
      });
    }
    // Clearing the executor and returning to todo hands the issue back to
    // routing, which picks an agent (same patch web sends).
    if (!settled && !(unassigned && ticket.status === "todo")) {
      actions.push({
        label: t("tickets.assign_agent"),
        run: () => run({ assignee_type: null, assignee_id: null, status: "todo" }),
      });
    }
    if (!settled) {
      actions.push({
        label: t("tickets.withdraw"),
        destructive: true,
        run: () => run({ status: "cancelled" }),
      });
    }
    if (sessionId) {
      actions.push({ label: t("tickets.remove"), run: remove });
    }
    const options = [...actions.map((a) => a.label), t("common:actions.cancel")];
    ActionSheetIOS.showActionSheetWithOptions(
      {
        title: `${ticket.identifier} ${ticket.title}`,
        options,
        cancelButtonIndex: options.length - 1,
        destructiveButtonIndex: actions.findIndex((a) => a.destructive),
      },
      (i) => {
        const action = actions[i];
        if (action) action.run();
      },
    );
  };

  return (
    <View className="min-h-11 flex-row items-center gap-2 pl-3">
      <StatusIcon
        status={ticket.status}
        category={category}
        icon={catalog.iconOf(ticket.status)}
        color={catalog.colorOf(ticket.status)}
        size={14}
      />
      <Pressable
        accessibilityRole="link"
        onPress={() => {
          if (wsSlug) router.push(`/${wsSlug}/issue/${ticket.id}`);
        }}
        className="min-w-0 flex-1 py-1.5 active:opacity-70"
      >
        <View className="min-w-0 flex-row items-baseline gap-1.5">
          <Text className="shrink-0 text-sm text-muted-foreground">
            {ticket.identifier}
          </Text>
          <Text className="shrink text-sm font-medium text-foreground" numberOfLines={1}>
            {ticket.title}
          </Text>
        </View>
        <Text className="text-xs text-muted-foreground" numberOfLines={1}>
          {detail}
        </Text>
      </Pressable>
      {settled && !sessionId ? (
        <View className="w-3" />
      ) : (
        <IconButton
          name="ellipsis-horizontal"
          iconSize={18}
          color={THEME[colorScheme].mutedForeground}
          className="h-11 w-11"
          disabled={update.isPending || unpin.isPending}
          onPress={openActions}
          accessibilityLabel={t("tickets.actions", { id: ticket.identifier })}
        />
      )}
    </View>
  );
}
