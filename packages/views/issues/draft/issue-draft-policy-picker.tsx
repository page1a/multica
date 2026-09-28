"use client";

import {
  ISSUE_DRAFT_POLICIES,
  type IssueDraftPolicyKey,
} from "@multica/core/issue-drafts";
import type { IssueDraftPolicy } from "@multica/core/types";
import {
  DropdownMenuRadioGroup,
  DropdownMenuRadioItem,
  DropdownMenuSeparator,
} from "@multica/ui/components/ui/dropdown-menu";
import { cn } from "@multica/ui/lib/utils";
import { useT } from "../../i18n";

/**
 * The label each policy key carries, as the message key it resolves to.
 *
 * A record rather than a ternary on `key === "question"`: `ISSUE_DRAFT_POLICIES`
 * is the whitelist this menu renders, so a key added there without a label here
 * is a compile error — not an option that quietly reads as plain dialogue, and
 * not a menu that has to be edited in three places to grow a fourth.
 */
const POLICY_LABELS = {
  question: "policy_guided",
  conversation: "policy_plain",
  frontend: "policy_frontend",
} as const satisfies Record<IssueDraftPolicyKey, string>;

/**
 * Which alignment policy the conversation runs under: the guided one that asks
 * one question at a time, plain dialogue, or the front-end look round that
 * settles a screen by building something the user can open and look at.
 *
 * Three named options rather than a switch, because these are three named
 * behaviours and not an on/off setting — "关闭引导" tells the user what they
 * lose, "普通对话" tells them what they get. The version caption below them is
 * the audit half: it names the prompt the carrier was actually given, which is
 * the answer to "why did the alignment ask that?" a week later.
 *
 * It is menu content, not a control strip: it lives in the alignment header's
 * ⋯ menu, so this renders radio items — the menu's own single-select primitive,
 * which keeps arrow keys, focus and dismissal working — and writes out the
 * label it used to inherit from the header it sat in.
 *
 * Renders nothing when the server reports no policy at all. An installed
 * desktop client can talk to a backend that predates policies, and offering a
 * switch that cannot land is worse than offering none.
 */
export function IssueDraftPolicyPicker({
  policy,
  switching,
  disabled,
  onChange,
}: {
  policy: IssueDraftPolicy;
  /** A switch is in flight: the picker must show the state the server has, not
   *  the one the user clicked. */
  switching: boolean;
  disabled: boolean;
  onChange: (policy: IssueDraftPolicyKey) => void;
}) {
  const { t } = useT("issues");
  if (!policy.key) return null;

  const locked = disabled || switching;

  return (
    <>
      <span className="px-1.5 py-1 text-caption font-medium text-muted-foreground">
        {t(($) => $.alignment.policy_label)}
      </span>
      <DropdownMenuRadioGroup
        aria-label={t(($) => $.alignment.policy_label)}
        // Controlled by what the server recorded, so a refused switch leaves
        // the running policy showing rather than the one that was clicked.
        value={policy.key}
        onValueChange={(next) => {
          if (next === policy.key) return;
          onChange(next as IssueDraftPolicyKey);
        }}
      >
        {ISSUE_DRAFT_POLICIES.map((key) => (
          <DropdownMenuRadioItem
            key={key}
            value={key}
            disabled={locked}
            // The checked state lives on weight and text colour, and its
            // focus is written out: a plain `focus:bg-accent` alone would
            // make the running option look exactly like the other one under
            // the cursor.
            className={cn(
              "text-muted-foreground",
              "data-checked:font-medium data-checked:text-foreground",
              "data-checked:focus:bg-accent data-checked:focus:text-foreground",
            )}
          >
            {t(($) => $.alignment[POLICY_LABELS[key]])}
          </DropdownMenuRadioItem>
        ))}
      </DropdownMenuRadioGroup>
      <DropdownMenuSeparator />
      <span
        className="block max-w-full truncate px-1.5 py-1 text-caption text-muted-foreground"
      >
        {t(($) => $.alignment.policy_version, {
          version: `${policy.key}@${policy.version}`,
        })}
      </span>
    </>
  );
}
