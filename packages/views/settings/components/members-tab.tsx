"use client";

import { useCallback, useEffect, useRef, useState } from "react";
import {
  AlertCircle,
  Clock,
  Copy,
  Crown,
  Eye,
  Link,
  Loader2,
  Mail,
  MoreHorizontal,
  Plus,
  Shield,
  Trash2,
  User,
  UserMinus,
  X,
} from "lucide-react";
import { ActorAvatar } from "../../common/actor-avatar";
import { useOptionalNavigation } from "../../navigation";
import type {
  Invitation,
  MemberRole,
  MemberWithUser,
  PurchaseWorkspaceSeatsRequest,
  ShareLink,
  WorkspaceSeatPurchasePreview,
} from "@multica/core/types";
import { Input } from "@multica/ui/components/ui/input";
import { Button } from "@multica/ui/components/ui/button";
import { Card, CardContent } from "@multica/ui/components/ui/card";
import { Badge } from "@multica/ui/components/ui/badge";
import {
  AlertDialog,
  AlertDialogContent,
  AlertDialogHeader,
  AlertDialogTitle,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogCancel,
  AlertDialogAction,
} from "@multica/ui/components/ui/alert-dialog";
import {
  Select,
  SelectTrigger,
  SelectValue,
  SelectContent,
  SelectItem,
} from "@multica/ui/components/ui/select";
import {
  DropdownMenu,
  DropdownMenuTrigger,
  DropdownMenuContent,
  DropdownMenuItem,
} from "@multica/ui/components/ui/dropdown-menu";
import { Skeleton } from "@multica/ui/components/ui/skeleton";
import { toast } from "sonner";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useAuthStore } from "@multica/core/auth";
import {
  usePreviewWorkspaceSeatPurchase,
  usePurchaseWorkspaceSeats,
  workspaceSubscriptionSummaryOptions,
} from "@multica/core/billing";
import { useWorkspaceId } from "@multica/core/hooks";
import { useCurrentWorkspace } from "@multica/core/paths";
import {
  invitationListOptions,
  memberListOptions,
  shareLinkListOptions,
  workspaceKeys,
} from "@multica/core/workspace/queries";
import {
  asMemberRole,
  roleChangeImpact,
  roleOptions,
  type RoleCapability,
  type RoleOption,
} from "@multica/core/workspace/member-roles";
import { api, errorCode } from "@multica/core/api";
import { useLocale, useT } from "../../i18n";
import { SettingsCard, SettingsSection, SettingsTab } from "./settings-layout";
import { formatStripeMinorAmount } from "./billing-format";
import {
  isSingleSeatInvitePreview,
  purchasedSeatIsReadyForInvitation,
  seatInvitationCanRetryAfterPurchase,
  seatInvitationCapacityFailure,
  seatPurchaseCanRetryWithSameQuote,
  seatPurchaseMatchesPreview,
} from "./seat-invite-purchase";

const SEAT_PURCHASE_CONFIRM_TIMEOUT_MS = 2 * 60_000;

type InviteSeatPurchase = {
  workspaceId: string;
  email: string;
  role: MemberRole;
  preview: WorkspaceSeatPurchasePreview;
  idempotencyKey: string;
  phase: "review" | "purchasing" | "waiting" | "inviting" | "error";
  submittedAt?: number;
  error?: string;
  retryable?: boolean;
};

function createSeatPurchaseKey(workspaceId: string): string {
  const suffix =
    globalThis.crypto?.randomUUID?.() ??
    `${Date.now()}-${Math.random().toString(36).slice(2)}`;
  return `invite-seat-${workspaceId}-${suffix}`.slice(0, 200);
}

const ROLE_ICONS: Record<MemberRole, typeof Crown> = {
  owner: Crown,
  admin: Shield,
  member: User,
  guest: Eye,
};

// Builds the shareable URL for a share-link invite. Prefers the navigation
// adapter's getShareableUrl (works on desktop where window.location.origin is
// not the public web origin), falling back to the browser origin on web.
function buildShareLinkUrl(
  navigation: ReturnType<typeof useOptionalNavigation>,
  code: string,
): string {
  const joinPath = `/join?code=${code}`;
  if (navigation?.getShareableUrl) {
    return navigation.getShareableUrl(joinPath);
  }
  return `${typeof window !== "undefined" ? window.location.origin : ""}${joinPath}`;
}

/** Joins capability phrases the way the locale joins list items. */
function listSeparator(locale: string): string {
  return locale.startsWith("zh") || locale.startsWith("ja") ? "\u3001" : ", ";
}

function useRoleLabels() {
  const { t } = useT("settings");
  return {
    owner: {
      label: t(($) => $.members.roles.owner.label),
      description: t(($) => $.members.roles.owner.description),
      icon: ROLE_ICONS.owner,
    },
    admin: {
      label: t(($) => $.members.roles.admin.label),
      description: t(($) => $.members.roles.admin.description),
      icon: ROLE_ICONS.admin,
    },
    member: {
      label: t(($) => $.members.roles.member.label),
      description: t(($) => $.members.roles.member.description),
      icon: ROLE_ICONS.member,
    },
    guest: {
      label: t(($) => $.members.roles.guest.label),
      description: t(($) => $.members.roles.guest.description),
      icon: ROLE_ICONS.guest,
    },
  } as const;
}

/** Why a tier is not selectable, in the words shown under it in the picker. */
function useRoleBlockLabels(): Record<
  NonNullable<RoleOption["block"]>,
  string
> {
  const { t } = useT("settings");
  return {
    last_owner: t(($) => $.members.cannot_demote_last_owner),
    owner_requires_owner: t(($) => $.members.role_blocked.owner_requires_owner),
  };
}

/** Capability phrases for the "what this change affects" summary. */
function useCapabilityLabels(): Record<RoleCapability, string> {
  const { t } = useT("settings");
  return {
    view_shared: t(($) => $.members.capabilities.view_shared),
    see_workspace_scope: t(($) => $.members.capabilities.see_workspace_scope),
    create_resources: t(($) => $.members.capabilities.create_resources),
    comment_and_edit: t(($) => $.members.capabilities.comment_and_edit),
    be_assigned: t(($) => $.members.capabilities.be_assigned),
    manage_members: t(($) => $.members.capabilities.manage_members),
    workspace_settings: t(($) => $.members.capabilities.workspace_settings),
    billing_and_transfer: t(($) => $.members.capabilities.billing_and_transfer),
  };
}

function MemberRow({
  member,
  canManage,
  canManageOwners,
  ownerCount,
  isSelf,
  busy,
  error,
  onRoleChange,
  onRemove,
}: {
  member: MemberWithUser;
  canManage: boolean;
  canManageOwners: boolean;
  /** Total number of owners in this workspace — needed to gate demoting the
   *  last owner per `workspace.go:497-507`. */
  ownerCount: number;
  isSelf: boolean;
  busy: boolean;
  /** Inline message for a save this row just failed, shown next to the
   *  picker that rolled back. */
  error: string | null;
  onRoleChange: (role: MemberRole) => void;
  onRemove: () => void;
}) {
  const { t } = useT("settings");
  const roleConfig = useRoleLabels();
  const blockLabels = useRoleBlockLabels();
  // The server's role enum is parsed leniently, so a backend that grows a
  // fifth tier reaches us as a string we cannot place on the ladder. Show it
  // as-is and refuse to edit rather than guess a tier for a real person.
  const role = asMemberRole(member.role);
  const rc = role ? roleConfig[role] : null;
  const RoleIcon = rc?.icon ?? User;
  const canEditRole =
    canManage && !isSelf && role !== null && (role !== "owner" || canManageOwners);
  const canRemove =
    canManage && !isSelf && (role !== "owner" || canManageOwners);
  const options = role
    ? roleOptions({
        current: role,
        actorIsOwner: canManageOwners,
        isLastOwner: role === "owner" && ownerCount <= 1,
      })
    : [];

  return (
    <div className="flex items-center gap-3 px-4 py-3">
      <ActorAvatar actorType="member" actorId={member.user_id} size="lg" />
      <div className="min-w-0 flex-1">
        <div className="text-body font-medium truncate">{member.name}</div>
        <div className="text-caption text-muted-foreground truncate">{member.email}</div>
        {error && (
          <div className="mt-1 flex items-start gap-1 text-caption text-destructive">
            <AlertCircle className="mt-px h-3 w-3 shrink-0" />
            <span className="min-w-0">{error}</span>
          </div>
        )}
      </div>
      {canEditRole && role ? (
        <Select
          items={options.map((option) => ({
            value: option.role,
            label: roleConfig[option.role].label,
          }))}
          value={role}
          disabled={busy}
          onValueChange={(value) => {
            const next = asMemberRole(value);
            if (next && next !== role) onRoleChange(next);
          }}
        >
          <SelectTrigger
            size="sm"
            className="w-36"
            aria-label={t(($) => $.members.role_select_label, { name: member.name })}
          >
            {busy ? (
              <Loader2 className="h-3.5 w-3.5 animate-spin text-muted-foreground" />
            ) : (
              <RoleIcon className="h-3.5 w-3.5 text-muted-foreground" />
            )}
            {/* The label stays on the optimistic tier while the write is in
                flight — swapping it for "saving" would hide the very change
                the row was just told to make. The spinner carries that. */}
            <SelectValue>{() => roleConfig[role].label}</SelectValue>
          </SelectTrigger>
          <SelectContent className="w-auto">
            {options.map((option) => {
              const config = roleConfig[option.role];
              const Icon = config.icon;
              return (
                <SelectItem
                  key={option.role}
                  value={option.role}
                  disabled={option.disabled}
                >
                  <Icon className="h-3.5 w-3.5" />
                  <div className="flex flex-col items-start">
                    <span>{config.label}</span>
                    <span className="text-caption text-muted-foreground font-normal">
                      {option.block ? blockLabels[option.block] : config.description}
                    </span>
                  </div>
                </SelectItem>
              );
            })}
          </SelectContent>
        </Select>
      ) : (
        <Badge
          variant="secondary"
          title={role ? undefined : t(($) => $.members.unknown_role_title)}
        >
          <RoleIcon className="h-3 w-3" />
          {rc ? rc.label : t(($) => $.members.unknown_role)}
        </Badge>
      )}
      {canRemove && (
        <DropdownMenu>
          <DropdownMenuTrigger
            render={
              <Button variant="ghost" size="icon-sm" disabled={busy}>
                <MoreHorizontal className="h-4 w-4 text-muted-foreground" />
              </Button>
            }
          />
          <DropdownMenuContent align="end" className="w-auto">
            <DropdownMenuItem variant="destructive" onClick={onRemove}>
              <UserMinus className="h-3.5 w-3.5" />
              {t(($) => $.members.remove_action)}
            </DropdownMenuItem>
          </DropdownMenuContent>
        </DropdownMenu>
      )}
    </div>
  );
}

/**
 * "Nobody here yet" prompt with the invite entry.
 *
 * The roster is never literally empty for the person reading it — you are
 * always in your own workspace — so the state that actually ships is "only
 * you", shown under your row. The truly-empty branch is kept for a roster
 * the API could not give us, where we also cannot prove the reader may
 * invite anyone.
 */
function MembersInvitePrompt({ onInvite }: { onInvite: () => void }) {
  const { t } = useT("settings");
  return (
    <Card>
      <CardContent className="flex flex-col items-center gap-2 py-8 text-center">
        <div className="text-body font-medium">{t(($) => $.members.empty_title)}</div>
        <p className="text-caption text-muted-foreground">
          {t(($) => $.members.empty_description)}
        </p>
        <Button variant="outline" className="mt-2" onClick={onInvite}>
          <Plus className="h-4 w-4" />
          {t(($) => $.members.empty_action)}
        </Button>
      </CardContent>
    </Card>
  );
}

/** Placeholder rows while the roster loads, so the section keeps its height
 *  instead of collapsing and then jumping. */
function MemberRowSkeleton() {
  return (
    <div className="flex items-center gap-3 px-4 py-3">
      <Skeleton className="h-8 w-8 rounded-full" />
      <div className="min-w-0 flex-1 space-y-1.5">
        <Skeleton className="h-3.5 w-32" />
        <Skeleton className="h-3 w-48" />
      </div>
      <Skeleton className="h-7 w-36 rounded-md" />
    </div>
  );
}

function InvitationRow({
  invitation,
  canManage,
  onRevoke,
  busy,
}: {
  invitation: Invitation;
  canManage: boolean;
  onRevoke: () => void;
  busy: boolean;
}) {
  const { t } = useT("settings");
  const roleConfig = useRoleLabels();
  const rc = roleConfig[invitation.role];

  return (
    <div className="flex items-center gap-3 px-4 py-3">
      <div className="flex h-8 w-8 items-center justify-center rounded-full bg-muted">
        <Mail className="h-4 w-4 text-muted-foreground" />
      </div>
      <div className="min-w-0 flex-1">
        <div className="text-body font-medium truncate">{invitation.invitee_email}</div>
        <div className="flex items-center gap-1 text-caption text-muted-foreground">
          <Clock className="h-3 w-3" />
          <span>{t(($) => $.members.pending_status)}</span>
        </div>
      </div>
      {canManage && (
        <Button
          variant="ghost"
          size="icon-sm"
          disabled={busy}
          onClick={onRevoke}
          title={t(($) => $.members.revoke_invitation_tooltip)}
        >
          <X className="h-4 w-4 text-muted-foreground" />
        </Button>
      )}
      <Badge variant="outline">
        {rc.label}
      </Badge>
    </div>
  );
}

function ShareLinkRow({
  link,
  onRevoke,
  busy,
  onCopy,
}: {
  link: ShareLink;
  onRevoke: () => void;
  busy: boolean;
  onCopy: () => void;
}) {
  const { t } = useT("settings");
  const locale = useLocale();
  const roleConfig = useRoleLabels();
  const rc = roleConfig[link.role];
  const navigation = useOptionalNavigation();
  const joinUrl = buildShareLinkUrl(navigation, link.code);

  return (
    <div className="flex items-center gap-3 px-4 py-3">
      <div className="flex h-8 w-8 items-center justify-center rounded-full bg-muted">
        <Link className="h-4 w-4 text-muted-foreground" />
      </div>
      <div className="min-w-0 flex-1">
        <div className="flex flex-wrap items-center gap-x-1 text-body font-medium">
          <span>{t(($) => $.members.share_link_uses, { used: link.use_count, max: link.max_uses ?? "∞" })}</span>
          {link.expires_at && <span>· {t(($) => $.members.share_link_expires, { date: new Date(link.expires_at).toLocaleDateString(locale) })}</span>}
        </div>
        <div
          className="truncate font-mono text-caption text-muted-foreground"
          title={joinUrl}
        >
          {joinUrl}
        </div>
      </div>
      <Button
        variant="ghost"
        size="icon-sm"
        onClick={onCopy}
        title={t(($) => $.members.share_link_copy_tooltip)}
      >
        <Copy className="h-4 w-4 text-muted-foreground" />
      </Button>
      <Button
        variant="ghost"
        size="icon-sm"
        disabled={busy}
        onClick={onRevoke}
        title={t(($) => $.members.share_link_revoke_tooltip)}
      >
        <Trash2 className="h-4 w-4 text-muted-foreground" />
      </Button>
      <Badge variant="outline">
        {rc.label}
      </Badge>
    </div>
  );
}

export function MembersTab() {
  const { t } = useT("settings");
  const { t: billingT } = useT("billing");
  const locale = useLocale();
  const roleConfig = useRoleLabels();
  const capabilityLabels = useCapabilityLabels();
  const user = useAuthStore((s) => s.user);
  const workspace = useCurrentWorkspace();
  const qc = useQueryClient();
  const wsId = useWorkspaceId();
  const navigation = useOptionalNavigation();
  const { data: members = [], isPending: membersLoading } = useQuery(
    memberListOptions(wsId),
  );
  const { data: invitations = [] } = useQuery(invitationListOptions(wsId));

  const [inviteEmail, setInviteEmail] = useState("");
  const inviteEmailRef = useRef<HTMLInputElement>(null);
  const [inviteRole, setInviteRole] = useState<MemberRole>("member");
  const [inviteLoading, setInviteLoading] = useState(false);
  const [inviteSeatPurchase, setInviteSeatPurchase] =
    useState<InviteSeatPurchase | null>(null);
  const dispatchedInvitePurchaseKey = useRef<string | null>(null);
  const [memberActionId, setMemberActionId] = useState<string | null>(null);
  /** Per-row save error, cleared when that row is retried. Keyed by member id
   *  so one failed save never blanks another row's message. */
  const [roleErrors, setRoleErrors] = useState<Record<string, string>>({});
  const [invitationActionId, setInvitationActionId] = useState<string | null>(null);
  const [shareLinkActionId, setShareLinkActionId] = useState<string | null>(null);
  const [shareLinkLoading, setShareLinkLoading] = useState(false);
  const [shareLinkRole, setShareLinkRole] = useState<MemberRole>("member");
  const [shareLinkExpiry, setShareLinkExpiry] = useState<string>("168"); // default 7 days
  const [confirmAction, setConfirmAction] = useState<{
    title: string;
    description: string;
    variant?: "destructive";
    onConfirm: () => Promise<void>;
  } | null>(null);
  const previewSeatPurchase = usePreviewWorkspaceSeatPurchase();
  const purchaseSeats = usePurchaseWorkspaceSeats(wsId);
  const seatPurchaseSummary = useQuery({
    ...workspaceSubscriptionSummaryOptions(wsId),
    enabled:
      inviteSeatPurchase?.phase === "waiting" &&
      inviteSeatPurchase.workspaceId === wsId,
    staleTime: 0,
    refetchInterval:
      inviteSeatPurchase?.phase === "waiting" ? 2_000 : false,
  });

  const currentMember = members.find((m) => m.user_id === user?.id) ?? null;
  const canManageWorkspace = currentMember?.role === "owner" || currentMember?.role === "admin";
  const isOwner = currentMember?.role === "owner";
  const ownerCount = members.filter((m) => m.role === "owner").length;
  // "No members" as a person experiences it: the roster holds nobody but
  // them. A literally empty roster carries no evidence about the reader's
  // own tier, so it cannot offer an invite it may not be allowed to make.
  const rosterIsJustYou =
    !membersLoading && members.every((m) => m.user_id === user?.id);
  // Only owners/admins may list share links; skip the request for plain
  // members (the server would 403) once the current member's role is known.
  const { data: shareLinks = [] } = useQuery(shareLinkListOptions(wsId, canManageWorkspace));

  const sendInvitation = useCallback(
    async (email: string, role: MemberRole) => {
      if (!workspace) return;
      await api.createMember(workspace.id, { email, role });
      setInviteEmail("");
      setInviteRole("member");
      await qc.invalidateQueries({
        queryKey: workspaceKeys.invitations(wsId),
      });
      toast.success(t(($) => $.members.toast_invitation_sent));
    },
    [qc, t, workspace, wsId],
  );

  const handleInviteMember = async () => {
    if (!workspace) return;
    const email = inviteEmail.trim();
    const role = inviteRole;
    setInviteLoading(true);
    try {
      await sendInvitation(email, role);
    } catch (e) {
      const code = errorCode(e);
      const capacityFailure = seatInvitationCapacityFailure(code);
      if (code === "seat_capacity_overcommitted") {
        try {
          const summary = await qc.fetchQuery({
            ...workspaceSubscriptionSummaryOptions(wsId),
            staleTime: 0,
          });
          const capacity = summary?.seatCapacity;
          toast.error(
            billingT(($) => $.workspace.seats.members_over_capacity_title),
            capacity
              ? {
                  description: billingT(
                    ($) => $.workspace.seats.occupancy_over_capacity_description,
                    {
                      // Cloud refuses on used + reserved, so pending
                      // invitations have to appear here: the most common
                      // trigger is a first ledger snapshot whose member count
                      // alone still fits inside the purchased seats.
                      occupied: capacity.used + capacity.reserved,
                      purchased: capacity.purchased,
                      members: capacity.used,
                      reserved: capacity.reserved,
                    },
                  ),
                }
              : undefined,
          );
        } catch {
          toast.error(
            billingT(($) => $.workspace.seats.members_over_capacity_title),
          );
        }
        return;
      }
      if (capacityFailure === "full") {
        try {
          const preview = await previewSeatPurchase.mutateAsync({
            additionalSeats: 1,
          });
          if (!isSingleSeatInvitePreview(preview)) {
            toast.error(
              billingT(($) => $.workspace.seat_purchase.preview_unreadable),
            );
            return;
          }
          setInviteSeatPurchase({
            workspaceId: workspace.id,
            email,
            role,
            preview,
            idempotencyKey: createSeatPurchaseKey(workspace.id),
            phase: "review",
          });
          dispatchedInvitePurchaseKey.current = null;
        } catch (previewError) {
          toast.error(
            errorCode(previewError) === "seat_purchase_in_progress"
              ? billingT(($) => $.workspace.seat_purchase.in_progress)
              : billingT(($) => $.workspace.seat_purchase.preview_failed),
          );
        }
        return;
      }
      if (capacityFailure === "unavailable") {
        toast.error(t(($) => $.members.toast_seat_capacity_unavailable));
        return;
      }
      if (capacityFailure === "rate_limited") {
        toast.error(t(($) => $.members.toast_seat_capacity_rate_limited));
        return;
      }
      toast.error(e instanceof Error ? e.message : t(($) => $.members.toast_invitation_failed));
    } finally {
      setInviteLoading(false);
    }
  };

  const handlePurchaseSeatAndInvite = async () => {
    if (
      !inviteSeatPurchase ||
      (inviteSeatPurchase.phase !== "review" &&
        !(inviteSeatPurchase.phase === "error" && inviteSeatPurchase.retryable))
    ) {
      return;
    }
    const current = inviteSeatPurchase;
    if (!workspace || current.workspaceId !== workspace.id) {
      setInviteSeatPurchase(null);
      return;
    }
    // A transient failure may happen after the purchase succeeded and the
    // first invitation dispatch ran. Reusing the same idempotency key is safe,
    // but the dispatch guard must be reopened for the retried invitation.
    dispatchedInvitePurchaseKey.current = null;
    setInviteSeatPurchase({
      ...current,
      phase: "purchasing",
      error: undefined,
      retryable: false,
    });
    const request: PurchaseWorkspaceSeatsRequest = {
      additionalSeats: current.preview.additionalSeats,
      expectedCurrentSeats: current.preview.currentSeats,
      expectedPurchaseVersion: current.preview.purchaseVersion,
      acceptedProrationAmount: current.preview.prorationAmount,
      currency: current.preview.currency,
      idempotencyKey: current.idempotencyKey,
    };
    try {
      const response = await purchaseSeats.mutateAsync(request);
      if (!seatPurchaseMatchesPreview(response, current.preview)) {
        setInviteSeatPurchase({
          ...current,
          phase: "error",
          error: billingT(
            ($) => $.workspace.seat_purchase.purchase_unreadable,
          ),
          retryable: true,
        });
        return;
      }
      const submittedAt = Date.now();
      setInviteSeatPurchase({
        ...current,
        phase: "waiting",
        submittedAt,
        error: undefined,
        retryable: false,
      });
      await qc.invalidateQueries({
        queryKey: workspaceSubscriptionSummaryOptions(wsId).queryKey,
      });
    } catch (error) {
      const code = errorCode(error);
      const message =
        code === "seat_purchase_payment_failed"
          ? billingT(($) => $.workspace.seat_purchase.payment_failed)
          : code === "seat_purchase_in_progress"
            ? billingT(($) => $.workspace.seat_purchase.in_progress)
            : code === "seat_quote_changed" || code === "seat_capacity_changed"
              ? billingT(($) => $.workspace.seat_purchase.quote_changed)
              : billingT(($) => $.workspace.seat_purchase.purchase_failed);
      setInviteSeatPurchase({
        ...current,
        phase: "error",
        error: message,
        retryable: seatPurchaseCanRetryWithSameQuote(code),
      });
    }
  };

  useEffect(() => {
    setInviteSeatPurchase((current) =>
      current && current.workspaceId !== wsId ? null : current,
    );
  }, [wsId]);

  useEffect(() => {
    const purchase = inviteSeatPurchase;
    if (
      purchase?.phase !== "waiting" ||
      purchase.workspaceId !== wsId ||
      purchase.submittedAt == null ||
      dispatchedInvitePurchaseKey.current === purchase.idempotencyKey ||
      !purchasedSeatIsReadyForInvitation(
        seatPurchaseSummary.data,
        purchase.preview,
        purchase.submittedAt,
        seatPurchaseSummary.dataUpdatedAt,
      )
    ) {
      return;
    }

    dispatchedInvitePurchaseKey.current = purchase.idempotencyKey;
    setInviteSeatPurchase({ ...purchase, phase: "inviting" });
    void sendInvitation(purchase.email, purchase.role)
      .then(() => setInviteSeatPurchase(null))
      .catch((error) => {
        const code = errorCode(error);
        const capacityFailure = seatInvitationCapacityFailure(code);
        let message: string;
        switch (capacityFailure) {
          case "full":
            message = t(($) => $.members.seat_purchase_capacity_taken);
            break;
          case "unavailable":
            message = t(($) => $.members.toast_seat_capacity_unavailable);
            break;
          case "rate_limited":
            message = t(($) => $.members.toast_seat_capacity_rate_limited);
            break;
          default:
            message =
              error instanceof Error
                ? error.message
                : t(($) => $.members.toast_invitation_failed);
        }
        setInviteSeatPurchase({
          ...purchase,
          phase: "error",
          retryable: seatInvitationCanRetryAfterPurchase(code),
          error: message,
        });
      });
  }, [
    inviteSeatPurchase,
    seatPurchaseSummary.data,
    seatPurchaseSummary.dataUpdatedAt,
    sendInvitation,
    t,
    wsId,
  ]);

  useEffect(() => {
    if (
      inviteSeatPurchase?.phase !== "waiting" ||
      inviteSeatPurchase.submittedAt == null
    ) {
      return;
    }
    const elapsed = Date.now() - inviteSeatPurchase.submittedAt;
    const timeout = window.setTimeout(() => {
      setInviteSeatPurchase((current) =>
        current?.phase === "waiting"
          ? {
              ...current,
              phase: "error",
              error: t(($) => $.members.seat_purchase_timeout),
              retryable: false,
            }
          : current,
      );
    }, Math.max(0, SEAT_PURCHASE_CONFIRM_TIMEOUT_MS - elapsed));
    return () => window.clearTimeout(timeout);
  }, [inviteSeatPurchase?.phase, inviteSeatPurchase?.submittedAt, t]);

  const handleRevokeInvitation = (invitation: Invitation) => {
    if (!workspace) return;
    setConfirmAction({
      title: t(($) => $.members.revoke_invitation_title),
      description: t(($) => $.members.revoke_invitation_description, { email: invitation.invitee_email }),
      variant: "destructive",
      onConfirm: async () => {
        setInvitationActionId(invitation.id);
        try {
          await api.revokeInvitation(workspace.id, invitation.id);
          qc.invalidateQueries({ queryKey: workspaceKeys.invitations(wsId) });
          toast.success(t(($) => $.members.toast_invitation_revoked));
        } catch (e) {
          toast.error(e instanceof Error ? e.message : t(($) => $.members.toast_invitation_revoke_failed));
        } finally {
          setInvitationActionId(null);
        }
      },
    });
  };

  /** Sentence naming what a tier change opened or closed, so the person
   *  making it sees the consequence and not just "saved". */
  const describeRoleImpact = (from: MemberRole, to: MemberRole): string => {
    const { gained, lost } = roleChangeImpact(from, to);
    const phrase = (caps: RoleCapability[]) =>
      caps.map((c) => capabilityLabels[c]).join(listSeparator(locale));
    const parts: string[] = [];
    if (gained.length > 0) {
      parts.push(t(($) => $.members.role_impact_gained, { items: phrase(gained) }));
    }
    if (lost.length > 0) {
      parts.push(t(($) => $.members.role_impact_lost, { items: phrase(lost) }));
    }
    return parts.length > 0
      ? parts.join(" ")
      : t(($) => $.members.role_impact_none);
  };

  const handleRoleChange = async (member: MemberWithUser, role: MemberRole) => {
    if (!workspace) return;
    const previousRole = asMemberRole(member.role);
    const key = workspaceKeys.members(wsId);
    const snapshot = qc.getQueryData<MemberWithUser[]>(key);
    // Optimistic per CLAUDE.md's field-patch rule: the new tier is what the
    // row will show, nobody navigates away, and the rollback is this one
    // field. Patch first so the picker never sits on the old value.
    qc.setQueryData<MemberWithUser[]>(key, (old) =>
      old?.map((m) => (m.id === member.id ? { ...m, role } : m)),
    );
    setRoleErrors((prev) => {
      if (!(member.id in prev)) return prev;
      const next = { ...prev };
      delete next[member.id];
      return next;
    });
    setMemberActionId(member.id);
    try {
      await api.updateMember(workspace.id, member.id, { role });
      toast.success(
        t(($) => $.members.role_impact_title, {
          name: member.name,
          role: roleConfig[role].label,
        }),
        {
          description: previousRole
            ? describeRoleImpact(previousRole, role)
            : undefined,
        },
      );
      qc.invalidateQueries({ queryKey: key });
    } catch (e) {
      // Roll the row back to exactly what the list held before the patch —
      // re-deriving it from `member` would lose a concurrent update that
      // arrived on the same list.
      if (snapshot) qc.setQueryData(key, snapshot);
      else qc.invalidateQueries({ queryKey: key });
      setRoleErrors((prev) => ({
        ...prev,
        [member.id]:
          e instanceof Error ? e.message : t(($) => $.members.toast_role_failed),
      }));
    } finally {
      setMemberActionId(null);
    }
  };

  const handleRemoveMember = (member: MemberWithUser) => {
    if (!workspace) return;
    setConfirmAction({
      title: t(($) => $.members.remove_member_title, { name: member.name }),
      description: t(($) => $.members.remove_member_description, { name: member.name, workspace: workspace.name }),
      variant: "destructive",
      onConfirm: async () => {
        setMemberActionId(member.id);
        try {
          await api.deleteMember(workspace.id, member.id);
          qc.invalidateQueries({ queryKey: workspaceKeys.members(wsId) });
          toast.success(t(($) => $.members.toast_member_removed));
        } catch (e) {
          toast.error(e instanceof Error ? e.message : t(($) => $.members.toast_member_remove_failed));
        } finally {
          setMemberActionId(null);
        }
      },
    });
  };

  const handleCreateShareLink = async () => {
    if (!workspace) return;
    setShareLinkLoading(true);
    try {
      await api.createShareLink(workspace.id, { role: shareLinkRole, expires_in: parseInt(shareLinkExpiry) || undefined });
      qc.invalidateQueries({ queryKey: workspaceKeys.shareLinks(wsId) });
      toast.success(t(($) => $.members.toast_share_link_created));
    } catch (e) {
      toast.error(e instanceof Error ? e.message : t(($) => $.members.toast_share_link_failed));
    } finally {
      setShareLinkLoading(false);
    }
  };

  const handleRevokeShareLink = (link: ShareLink) => {
    if (!workspace) return;
    setShareLinkActionId(link.id);
    api.revokeShareLink(workspace.id, link.id)
      .then(() => {
        qc.invalidateQueries({ queryKey: workspaceKeys.shareLinks(wsId) });
        toast.success(t(($) => $.members.toast_share_link_revoked));
      })
      .catch((e) => {
        toast.error(e instanceof Error ? e.message : t(($) => $.members.toast_share_link_revoke_failed));
      })
      .finally(() => setShareLinkActionId(null));
  };

  const handleCopyShareLink = (link: ShareLink) => {
    const joinUrl = buildShareLinkUrl(navigation, link.code);
    if (navigator.clipboard && window.isSecureContext) {
      navigator.clipboard.writeText(joinUrl).then(
        () => toast.success(t(($) => $.members.toast_share_link_copied)),
        () => toast.error(t(($) => $.members.toast_share_link_copy_failed)),
      );
    } else {
      const textArea = document.createElement("textarea");
      textArea.value = joinUrl;
      textArea.style.position = "fixed";
      textArea.style.left = "-9999px";
      document.body.appendChild(textArea);
      textArea.select();
      try {
        document.execCommand("copy");
        toast.success(t(($) => $.members.toast_share_link_copied));
      } catch {
        toast.error(t(($) => $.members.toast_share_link_copy_failed));
      }
      document.body.removeChild(textArea);
    }
  };

  if (!workspace) return null;

  return (
    <SettingsTab title={t(($) => $.page.tabs.members)}>
      <SettingsSection title={t(($) => $.members.section_title, { count: members.length })}>

        {canManageWorkspace && (
          <Card>
            <CardContent className="space-y-3">
              <div className="flex items-center gap-2">
                <Plus className="h-4 w-4 text-muted-foreground" />
                <h3 className="text-body font-medium">{t(($) => $.members.invite_title)}</h3>
              </div>
              <div className="grid gap-3 sm:grid-cols-[1fr_120px_auto]">
                <Input
                  ref={inviteEmailRef}
                  type="email"
                  name="invite-email"
                  autoComplete="email"
                  spellCheck={false}
                  aria-label={t(($) => $.members.invite_email_placeholder)}
                  value={inviteEmail}
                  onChange={(e) => setInviteEmail(e.target.value)}
                  placeholder={t(($) => $.members.invite_email_placeholder)}
                  onKeyDown={(e) => {
                    if (e.key === "Enter" && inviteEmail.trim()) handleInviteMember();
                  }}
                />
                <Select
                  items={(["member", "admin"] as const).map((value) => ({
                    value,
                    label: roleConfig[value].label,
                  }))}
                  value={inviteRole}
                  onValueChange={(value) => setInviteRole(value as MemberRole)}
                >
                  <SelectTrigger size="sm">
                    <SelectValue>{() => roleConfig[inviteRole].label}</SelectValue>
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value="member">{roleConfig.member.label}</SelectItem>
                    <SelectItem value="admin">{roleConfig.admin.label}</SelectItem>
                  </SelectContent>
                </Select>
                <Button
                  onClick={handleInviteMember}
                  disabled={inviteLoading || !inviteEmail.trim()}
                >
                  {inviteLoading ? t(($) => $.members.inviting) : t(($) => $.members.invite_button)}
                </Button>
              </div>
            </CardContent>
          </Card>
        )}

        {membersLoading ? (
          <div role="status" aria-label={t(($) => $.members.members_loading)}>
            <SettingsCard>
              {[0, 1, 2].map((i) => (
                <MemberRowSkeleton key={i} />
              ))}
            </SettingsCard>
          </div>
        ) : members.length > 0 ? (
          <SettingsCard>
            {members.map((m) => (
              <div key={m.id}>
                <MemberRow
                  member={m}
                  canManage={canManageWorkspace}
                  canManageOwners={isOwner}
                  ownerCount={ownerCount}
                  isSelf={m.user_id === user?.id}
                  busy={memberActionId === m.id}
                  error={roleErrors[m.id] ?? null}
                  onRoleChange={(role) => handleRoleChange(m, role)}
                  onRemove={() => handleRemoveMember(m)}
                />
              </div>
            ))}
          </SettingsCard>
        ) : (
          <p className="text-body text-muted-foreground">{t(($) => $.members.no_members)}</p>
        )}

        {rosterIsJustYou && canManageWorkspace && (
          <MembersInvitePrompt onInvite={() => inviteEmailRef.current?.focus()} />
        )}
      </SettingsSection>

      {invitations.length > 0 && (
        <SettingsSection title={t(($) => $.members.pending_title, { count: invitations.length })}>
          <SettingsCard>
            {invitations.map((inv) => (
              <div key={inv.id}>
                <InvitationRow
                  invitation={inv}
                  canManage={canManageWorkspace}
                  onRevoke={() => handleRevokeInvitation(inv)}
                  busy={invitationActionId === inv.id}
                />
              </div>
            ))}
          </SettingsCard>
        </SettingsSection>
      )}

      {canManageWorkspace && (
        <SettingsSection title={t(($) => $.members.share_links_title, { count: shareLinks.length })}>
          <Card>
            <CardContent className="space-y-3">
              <div className="flex items-center gap-2">
                <Link className="h-4 w-4 text-muted-foreground" />
                <h3 className="text-body font-medium">{t(($) => $.members.share_links_create_title)}</h3>
              </div>
              <div className="flex flex-wrap items-center gap-x-3 gap-y-2">
                <div className="flex min-w-0 flex-1 basis-40 items-center gap-2">
                  <span className="text-body text-muted-foreground shrink-0">{t(($) => $.members.role_field)}</span>
                  <Select
                    items={(["member", "admin"] as const).map((value) => ({
                      value,
                      label: roleConfig[value].label,
                    }))}
                    value={shareLinkRole}
                    onValueChange={(value) => setShareLinkRole(value as MemberRole)}
                  >
                    <SelectTrigger size="sm">
                      <SelectValue>{() => roleConfig[shareLinkRole].label}</SelectValue>
                    </SelectTrigger>
                    <SelectContent className="min-w-0">
                      <SelectItem value="member">{roleConfig.member.label}</SelectItem>
                      <SelectItem value="admin">{roleConfig.admin.label}</SelectItem>
                    </SelectContent>
                  </Select>
                </div>
                <div className="flex min-w-0 flex-1 basis-40 items-center gap-2">
                  <span className="text-body text-muted-foreground shrink-0">{t(($) => $.members.expiry_field)}</span>
                  <Select
                    items={[
                      { value: "24", label: t(($) => $.members.expiry_24h) },
                      { value: "168", label: t(($) => $.members.expiry_7d) },
                      { value: "720", label: t(($) => $.members.expiry_30d) },
                      { value: "0", label: t(($) => $.members.expiry_never) },
                    ]}
                    value={shareLinkExpiry}
                    onValueChange={(v) => v && setShareLinkExpiry(v)}
                  >
                    <SelectTrigger size="sm">
                      <SelectValue>{() => {
                        const opts: Record<string, string> = {
                          "24": t(($) => $.members.expiry_24h),
                          "168": t(($) => $.members.expiry_7d),
                          "720": t(($) => $.members.expiry_30d),
                          "0": t(($) => $.members.expiry_never),
                        };
                        return opts[shareLinkExpiry] || shareLinkExpiry;
                      }}</SelectValue>
                    </SelectTrigger>
                    <SelectContent className="min-w-0">
                      <SelectItem value="24">{t(($) => $.members.expiry_24h)}</SelectItem>
                      <SelectItem value="168">{t(($) => $.members.expiry_7d)}</SelectItem>
                      <SelectItem value="720">{t(($) => $.members.expiry_30d)}</SelectItem>
                      <SelectItem value="0">{t(($) => $.members.expiry_never)}</SelectItem>
                    </SelectContent>
                  </Select>
                </div>
                <Button onClick={handleCreateShareLink} disabled={shareLinkLoading} className="shrink-0">
                  {shareLinkLoading ? t(($) => $.members.share_links_creating) : t(($) => $.members.share_links_create_button)}
                </Button>
              </div>
            </CardContent>
          </Card>
          {shareLinks.length > 0 && (
            <SettingsCard>
              {shareLinks.map((link) => (
                <div key={link.id}>
                  <ShareLinkRow
                    link={link}
                    onRevoke={() => handleRevokeShareLink(link)}
                    busy={shareLinkActionId === link.id}
                    onCopy={() => handleCopyShareLink(link)}
                  />
                </div>
              ))}
            </SettingsCard>
          )}
        </SettingsSection>
      )}

      <AlertDialog
        open={inviteSeatPurchase !== null}
        onOpenChange={(open) => {
          if (
            !open &&
            (inviteSeatPurchase?.phase === "review" ||
              inviteSeatPurchase?.phase === "error")
          ) {
            setInviteSeatPurchase(null);
          }
        }}
      >
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>
              {t(($) => $.members.seat_purchase_title)}
            </AlertDialogTitle>
            <AlertDialogDescription>
              {inviteSeatPurchase?.phase === "review"
                ? t(($) => $.members.seat_purchase_description, {
                    email: inviteSeatPurchase.email,
                  })
                : inviteSeatPurchase?.phase === "error"
                  ? inviteSeatPurchase.error
                  : t(($) => $.members.seat_purchase_waiting)}
            </AlertDialogDescription>
          </AlertDialogHeader>

          {inviteSeatPurchase?.phase === "review" && (
            <div className="divide-y rounded-lg border text-body">
              <div className="flex items-center justify-between gap-4 px-4 py-3">
                <span className="text-muted-foreground">
                  {billingT(($) => $.workspace.seat_purchase.seats_after)}
                </span>
                <span className="font-medium">
                  {billingT(($) => $.workspace.seats.seat_count, {
                    count: inviteSeatPurchase.preview.resultingSeats,
                  })}
                </span>
              </div>
              <div className="flex items-center justify-between gap-4 px-4 py-3">
                <span className="text-muted-foreground">
                  {billingT(($) => $.workspace.seat_purchase.charge_today)}
                </span>
                <span className="font-medium">
                  {formatStripeMinorAmount(
                    inviteSeatPurchase.preview.prorationAmount,
                    inviteSeatPurchase.preview.currency,
                    locale,
                  ) ?? "—"}
                </span>
              </div>
              <div className="flex items-center justify-between gap-4 px-4 py-3">
                <span className="text-muted-foreground">
                  {billingT(($) => $.workspace.seat_purchase.next_invoice)}
                </span>
                <span className="font-medium">
                  {formatStripeMinorAmount(
                    inviteSeatPurchase.preview.nextInvoiceAmount,
                    inviteSeatPurchase.preview.currency,
                    locale,
                  ) ?? "—"}
                </span>
              </div>
              <p className="px-4 py-3 text-caption text-muted-foreground">
                {billingT(($) => $.workspace.seat_purchase.tax_notice)}
              </p>
            </div>
          )}

          {(inviteSeatPurchase?.phase === "purchasing" ||
            inviteSeatPurchase?.phase === "waiting" ||
            inviteSeatPurchase?.phase === "inviting") && (
            <div className="flex items-center gap-2 text-body text-muted-foreground">
              <Loader2 className="h-4 w-4 animate-spin" />
              {inviteSeatPurchase.phase === "inviting"
                ? t(($) => $.members.inviting)
                : t(($) => $.members.seat_purchase_waiting)}
            </div>
          )}

          <AlertDialogFooter>
            {(inviteSeatPurchase?.phase === "review" ||
              inviteSeatPurchase?.phase === "error") && (
              <AlertDialogCancel>
                {t(($) => $.members.confirm_cancel)}
              </AlertDialogCancel>
            )}
            {inviteSeatPurchase?.phase === "review" && (
              <AlertDialogAction
                onClick={(event) => {
                  event.preventDefault();
                  void handlePurchaseSeatAndInvite();
                }}
              >
                {t(($) => $.members.purchase_seat_and_invite)}
              </AlertDialogAction>
            )}
            {inviteSeatPurchase?.phase === "error" &&
              inviteSeatPurchase.retryable && (
                <AlertDialogAction
                  onClick={(event) => {
                    event.preventDefault();
                    void handlePurchaseSeatAndInvite();
                  }}
                >
                  {t(($) => $.members.retry_seat_purchase)}
                </AlertDialogAction>
              )}
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>

      <AlertDialog open={!!confirmAction} onOpenChange={(v) => { if (!v) setConfirmAction(null); }}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>{confirmAction?.title}</AlertDialogTitle>
            <AlertDialogDescription>{confirmAction?.description}</AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>{t(($) => $.members.confirm_cancel)}</AlertDialogCancel>
            <AlertDialogAction
              variant={confirmAction?.variant === "destructive" ? "destructive" : "default"}
              onClick={async () => {
                await confirmAction?.onConfirm();
                setConfirmAction(null);
              }}
            >
              {t(($) => $.members.confirm_action)}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </SettingsTab>
  );
}
