"use client";

import { HomePage } from "../../home/components/home-page";
import { useNavigation } from "../../navigation";
import { InboxActivityPage } from "./inbox-page";
import { isActivityLayer } from "./inbox-view";

/** Picks the inbox layer from the URL: the board by default, the list on request. */
export function InboxPage() {
  const { searchParams } = useNavigation();
  return isActivityLayer(searchParams) ? <InboxActivityPage /> : <HomePage />;
}
