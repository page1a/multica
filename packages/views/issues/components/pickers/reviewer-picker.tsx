"use client";

import { useState } from "react";
import { CircleSlash, UserMinus } from "lucide-react";
import type { IssueReviewerType, UpdateIssueRequest } from "@multica/core/types";
import { useQuery } from "@tanstack/react-query";
import { useActorName } from "@multica/core/workspace/hooks";
import { useWorkspaceId } from "@multica/core/hooks";
import { memberListOptions, agentListOptions } from "@multica/core/workspace/queries";
import { ActorAvatar } from "../../../common/actor-avatar";
import { DeferredPopup } from "../../../common/deferred-popup";
import {
  PropertyPicker,
  PickerItem,
  PickerSection,
  PickerEmpty,
  PICKER_TRIGGER_CLASS,
} from "./property-picker";
import { useT } from "../../../i18n";
import { matchesPinyin } from "../../../editor/extensions/pinyin-match";

interface ReviewerPickerProps {
  reviewerType: IssueReviewerType | null;
  reviewerId: string | null;
  /** Batch selection spanning different reviewers: no row is checked. */
  mixed?: boolean;
  onUpdate: (updates: Partial<UpdateIssueRequest>) => void;
  trigger?: React.ReactNode;
  triggerRender?: React.ReactElement<Record<string, unknown>>;
  open?: boolean;
  onOpenChange?: (v: boolean) => void;
  align?: "start" | "center" | "end";
}

/**
 * 验收席 — who accepts this issue once it reaches in_review.
 *
 * Deliberately mirrors AssigneePicker rather than reusing it: the value sets
 * are different (no squads, plus a "needs no review" row) and the gating is
 * different. Naming a reviewer does not dispatch work — the handoff happens
 * later, through the normal assignment path — so an agent with no runtime
 * bound is a legitimate choice here even though it cannot be assigned work
 * today.
 *
 * Three rows carry three distinct meanings, and collapsing any two would lose
 * information: "undecided" (routing may still fill it), "no review needed" (a
 * recorded decision that closes the question), and a named actor.
 */
export function ReviewerPicker(props: ReviewerPickerProps) {
  const hasDeferredTriggerContent =
    props.trigger !== undefined || props.triggerRender?.props.children != null;
  const canDefer =
    props.open === undefined &&
    props.onOpenChange === undefined &&
    hasDeferredTriggerContent;
  if (!canDefer) {
    return <ReviewerPickerImpl {...props} />;
  }
  return (
    <DeferredPopup
      trigger={props.trigger}
      triggerRender={props.triggerRender}
      triggerClassName={PICKER_TRIGGER_CLASS}
    >
      {(open, onOpenChange) => (
        <ReviewerPickerImpl {...props} open={open} onOpenChange={onOpenChange} />
      )}
    </DeferredPopup>
  );
}

function ReviewerPickerImpl({
  reviewerType,
  reviewerId,
  mixed = false,
  onUpdate,
  trigger: customTrigger,
  triggerRender,
  open: controlledOpen,
  onOpenChange: controlledOnOpenChange,
  align,
}: ReviewerPickerProps) {
  const { t } = useT("issues");
  const [internalOpen, setInternalOpen] = useState(false);
  const open = controlledOpen ?? internalOpen;
  const setOpen = controlledOnOpenChange ?? setInternalOpen;
  const [filter, setFilter] = useState("");
  const wsId = useWorkspaceId();
  const { data: members = [] } = useQuery(memberListOptions(wsId));
  const { data: agents = [] } = useQuery(agentListOptions(wsId));
  const { getActorName } = useActorName();

  const query = filter.trim().toLowerCase();
  const filteredMembers = members.filter(
    (m) => m.name.toLowerCase().includes(query) || matchesPinyin(m.name, query),
  );
  const filteredAgents = agents.filter(
    (a) =>
      !a.archived_at &&
      (a.name.toLowerCase().includes(query) || matchesPinyin(a.name, query)),
  );

  const isSelected = (type: string, id: string) =>
    reviewerType === type && reviewerId === id;

  const named = reviewerType != null && reviewerType !== "none" && reviewerId != null;

  const select = (updates: Partial<UpdateIssueRequest>) => {
    onUpdate(updates);
    setOpen(false);
  };

  return (
    <PropertyPicker
      open={open}
      onOpenChange={(v: boolean) => {
        setOpen(v);
        if (!v) setFilter("");
      }}
      width="w-64"
      align={align}
      searchable
      searchPlaceholder={t(($) => $.pickers.reviewer.search_placeholder)}
      onSearchChange={setFilter}
      triggerRender={triggerRender}
      trigger={
        customTrigger ? (
          customTrigger
        ) : named ? (
          <>
            <ActorAvatar actorType={reviewerType} actorId={reviewerId} size="sm" enableHoverCard showStatusDot />
            <span className="truncate">{getActorName(reviewerType, reviewerId)}</span>
          </>
        ) : reviewerType === "none" ? (
          <span className="truncate">{t(($) => $.pickers.reviewer.trigger_no_review)}</span>
        ) : (
          <span className="text-muted-foreground">{t(($) => $.pickers.reviewer.trigger_undecided)}</span>
        )
      }
    >
      {/* Undecided — the empty value, first row like every other picker. */}
      <PickerItem
        emptyValue
        selected={!mixed && reviewerType == null}
        onClick={() => select({ reviewer_type: null, reviewer_id: null })}
      >
        <UserMinus className="h-3.5 w-3.5 text-muted-foreground" />
        <span className="text-muted-foreground">{t(($) => $.pickers.reviewer.trigger_undecided)}</span>
      </PickerItem>

      {/* "No review needed" is a written answer, not an absence — it sits
          with the real values, below the empty row. */}
      <PickerItem
        selected={!mixed && reviewerType === "none"}
        onClick={() => select({ reviewer_type: "none", reviewer_id: null })}
      >
        <CircleSlash className="h-3.5 w-3.5 text-muted-foreground" />
        <span>{t(($) => $.pickers.reviewer.no_review)}</span>
      </PickerItem>

      {filteredMembers.length > 0 && (
        <PickerSection label={t(($) => $.pickers.reviewer.members_group)}>
          {filteredMembers.map((m) => (
            <PickerItem
              key={m.user_id}
              selected={isSelected("member", m.user_id)}
              onClick={() => select({ reviewer_type: "member", reviewer_id: m.user_id })}
            >
              <ActorAvatar actorType="member" actorId={m.user_id} size="sm" />
              <span className="truncate">{m.name}</span>
            </PickerItem>
          ))}
        </PickerSection>
      )}

      {filteredAgents.length > 0 && (
        <PickerSection label={t(($) => $.pickers.reviewer.agents_group)}>
          {filteredAgents.map((a) => (
            <PickerItem
              key={a.id}
              selected={isSelected("agent", a.id)}
              onClick={() => select({ reviewer_type: "agent", reviewer_id: a.id })}
            >
              <ActorAvatar actorType="agent" actorId={a.id} size="sm" showStatusDot />
              <span className="truncate">{a.name}</span>
            </PickerItem>
          ))}
        </PickerSection>
      )}

      {filteredMembers.length === 0 && filteredAgents.length === 0 && filter && <PickerEmpty />}
    </PropertyPicker>
  );
}
