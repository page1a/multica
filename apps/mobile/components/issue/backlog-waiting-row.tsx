/**
 * What a backlog ticket waits for (DENE-1638), inside `IssueHeaderCard`.
 *
 * Mirrors the 在等 row in the web sidebar: reads
 * `metadata["backlog.waiting_for"]`, renders only while the ticket is in
 * backlog, wraps instead of truncating.
 */
import { backlogWaitingFor } from "@multica/core/issues/backlog-waiting-for";
import type { Issue } from "@multica/core/types";
import { Text } from "@/components/ui/text";
import { useT } from "@/lib/i18n";

export function BacklogWaitingRow({ issue }: { issue: Issue }) {
  const { t } = useT("issues");
  const reason = backlogWaitingFor(issue);
  if (!reason) return null;
  return (
    <Text className="text-sm text-muted-foreground">
      {t("waiting_for", { reason })}
    </Text>
  );
}
