"use client";

import { useIsCompact } from "@multica/ui/hooks/use-mobile";
import { HomePage } from "../../home/components/home-page";
import { useNavigation } from "../../navigation";
import { InboxActivityPage } from "./inbox-page";
import { isActivityLayer } from "./inbox-view";

/**
 * Wide screens show one page: the notification list beside the board
 * (DENE-1004). Compact widths keep the two layers — the board by default, the
 * list on request.
 */
export function InboxPage() {
  const { searchParams } = useNavigation();
  const isCompact = useIsCompact();
  if (!isCompact) return <InboxActivityPage merged />;
  return isActivityLayer(searchParams) ? <InboxActivityPage /> : <HomePage />;
}
