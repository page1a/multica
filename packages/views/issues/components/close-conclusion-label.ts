import { useT } from "../../i18n";

/**
 * Human label for a `close.conclusion` value (DENE-1002).
 *
 * The record's own vocabulary is `delivered` / `blocked` / `awaiting_review` /
 * `awaiting_human` / `deferred` / `continuing`. Two places render it — the
 * sub-issue strip and the issue detail's close-record section — so the mapping
 * lives here instead of in each one. An unknown value (a future conclusion the
 * client predates) falls back to the raw key rather than disappearing.
 */
export function useCloseConclusionLabel(): (conclusion: string | null) => string | null {
  const { t } = useT("issues");
  return (conclusion) => {
    if (conclusion === null) return null;
    switch (conclusion) {
      case "delivered":
        return t(($) => $.close_protocol.conclusion_delivered);
      case "blocked":
        return t(($) => $.close_protocol.conclusion_blocked);
      case "awaiting_review":
        return t(($) => $.close_protocol.conclusion_awaiting_review);
      case "awaiting_human":
        return t(($) => $.close_protocol.conclusion_awaiting_human);
      case "deferred":
        return t(($) => $.close_protocol.conclusion_deferred);
      case "continuing":
        return t(($) => $.close_protocol.conclusion_continuing);
      default:
        return conclusion;
    }
  };
}
