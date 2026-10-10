/**
 * Why a done ticket skipped acceptance (DENE-1678), inside `IssueHeaderCard`.
 *
 * Mirrors the 跳过验收 row in the web sidebar: reads `metadata.review_skip`,
 * renders only while the ticket is done, wraps instead of truncating.
 */
import { reviewSkipReason } from "@multica/core/issues/review-skip";
import type { Issue } from "@multica/core/types";
import { Text } from "@/components/ui/text";
import { useT } from "@/lib/i18n";

export function ReviewSkipRow({ issue }: { issue: Issue }) {
  const { t } = useT("issues");
  const reason = reviewSkipReason(issue);
  if (!reason) return null;
  return (
    <Text className="text-sm text-muted-foreground">
      {t("review_skip", { reason })}
    </Text>
  );
}
