"use client";

import {
  DndContext,
  KeyboardSensor,
  PointerSensor,
  closestCenter,
  useSensor,
  useSensors,
  type DragEndEvent,
} from "@dnd-kit/core";
import {
  SortableContext,
  arrayMove,
  sortableKeyboardCoordinates,
  useSortable,
  verticalListSortingStrategy,
} from "@dnd-kit/sortable";
import { CSS } from "@dnd-kit/utilities";
import { GripVertical, Pin, PinOff } from "lucide-react";
import { Button } from "@multica/ui/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@multica/ui/components/ui/dialog";
import { cn } from "@multica/ui/lib/utils";
import {
  arrangeWorkspaces,
  useWorkspaceSwitcherPreference,
  useWorkspaceSwitcherPreferenceStore,
} from "@multica/core/workspace/switcher-preference";
import type { Workspace } from "@multica/core/types";
import { WorkspaceAvatar } from "../workspace/workspace-avatar";
import { useT } from "../i18n";

// The boundary between the two groups is itself a sortable entry: dropping a
// row above it pins, below it unpins. One flat list keeps dnd-kit on its
// simplest path (no cross-container bookkeeping) while still reading as two
// groups.
const DIVIDER_ID = "__workspace-organize-divider__";

/**
 * Drag-to-arrange for the workspace switcher. Lives in a dialog rather than
 * the dropdown itself: the dropdown closes on any click, so dragging inside it
 * keeps turning into an accidental workspace switch.
 */
export function WorkspaceOrganizeDialog({
  open,
  onOpenChange,
  workspaces,
  userId,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  workspaces: readonly Workspace[];
  userId: string;
}) {
  const { t } = useT("layout");
  const preference = useWorkspaceSwitcherPreference(userId);
  const setArrangement = useWorkspaceSwitcherPreferenceStore((s) => s.setArrangement);
  const { pinned, rest } = arrangeWorkspaces(workspaces, preference);
  const byId = new Map(workspaces.map((ws) => [ws.id, ws]));
  const items = [...pinned.map((ws) => ws.id), DIVIDER_ID, ...rest.map((ws) => ws.id)];

  const sensors = useSensors(
    useSensor(PointerSensor, { activationConstraint: { distance: 5 } }),
    useSensor(KeyboardSensor, { coordinateGetter: sortableKeyboardCoordinates }),
  );

  const commit = (next: string[]) => {
    const split = next.indexOf(DIVIDER_ID);
    setArrangement(userId, next.slice(0, split), next.slice(split + 1));
  };

  const handleDragEnd = ({ active, over }: DragEndEvent) => {
    if (!over || active.id === over.id) return;
    const from = items.indexOf(String(active.id));
    const to = items.indexOf(String(over.id));
    if (from === -1 || to === -1) return;
    commit(arrayMove(items, from, to));
  };

  // Pinning from the button moves the row to the bottom of the pinned group;
  // unpinning moves it to the top of the rest — the spot nearest the divider.
  const togglePin = (id: string) => {
    const without = items.filter((item) => item !== id);
    const split = without.indexOf(DIVIDER_ID);
    const isPinned = items.indexOf(id) < items.indexOf(DIVIDER_ID);
    without.splice(isPinned ? split + 1 : split, 0, id);
    commit(without);
  };

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{t(($) => $.sidebar.organize_title)}</DialogTitle>
          <DialogDescription>{t(($) => $.sidebar.organize_description)}</DialogDescription>
        </DialogHeader>
        <div className="-mx-1 flex max-h-[60vh] flex-col overflow-y-auto px-1">
          <GroupHeading>{t(($) => $.sidebar.pinned_label)}</GroupHeading>
          <DndContext sensors={sensors} collisionDetection={closestCenter} onDragEnd={handleDragEnd}>
            <SortableContext items={items} strategy={verticalListSortingStrategy}>
              {items.map((id) => {
                if (id === DIVIDER_ID) {
                  return (
                    <Divider
                      key={id}
                      emptyHint={pinned.length === 0 ? t(($) => $.sidebar.organize_pin_hint) : null}
                      label={t(($) => $.sidebar.organize_rest_label)}
                    />
                  );
                }
                const ws = byId.get(id);
                if (!ws) return null;
                const isPinned = pinned.some((p) => p.id === id);
                return (
                  <OrganizeRow
                    key={id}
                    workspace={ws}
                    pinned={isPinned}
                    dragLabel={t(($) => $.sidebar.organize_drag, { name: ws.name })}
                    pinLabel={isPinned ? t(($) => $.sidebar.unpin_workspace) : t(($) => $.sidebar.pin_workspace)}
                    onTogglePin={() => togglePin(id)}
                  />
                );
              })}
            </SortableContext>
          </DndContext>
        </div>
        <DialogFooter className="items-center sm:justify-between">
          <span className="text-caption text-muted-foreground">{t(($) => $.sidebar.organize_autosave)}</span>
          <Button onClick={() => onOpenChange(false)}>{t(($) => $.sidebar.organize_done)}</Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

function GroupHeading({ children }: { children: React.ReactNode }) {
  return <div className="px-2 pt-2 pb-1 text-caption text-muted-foreground">{children}</div>;
}

function Divider({ emptyHint, label }: { emptyHint: string | null; label: string }) {
  const { setNodeRef, transform, transition } = useSortable({
    id: DIVIDER_ID,
    disabled: { draggable: true, droppable: false },
  });
  return (
    <div ref={setNodeRef} style={{ transform: CSS.Transform.toString(transform), transition }}>
      {emptyHint && (
        <div className="mx-1 my-1 rounded-md border border-dashed border-border px-2 py-2 text-center text-caption text-muted-foreground">
          {emptyHint}
        </div>
      )}
      <GroupHeading>{label}</GroupHeading>
    </div>
  );
}

function OrganizeRow({
  workspace,
  pinned,
  dragLabel,
  pinLabel,
  onTogglePin,
}: {
  workspace: Workspace;
  pinned: boolean;
  dragLabel: string;
  pinLabel: string;
  onTogglePin: () => void;
}) {
  const { attributes, listeners, setNodeRef, transform, transition, isDragging } = useSortable({
    id: workspace.id,
  });
  return (
    <div
      ref={setNodeRef}
      style={{ transform: CSS.Transform.toString(transform), transition }}
      className={cn(
        "relative flex items-center gap-2 rounded-md bg-surface-raised py-1 pr-1 pl-0.5 motion-reduce:transition-none!",
        isDragging && "z-10 shadow-[var(--surface-shadow)]",
      )}
    >
      <button
        type="button"
        aria-label={dragLabel}
        className="flex size-6 shrink-0 touch-none cursor-grab items-center justify-center rounded-md text-muted-foreground focus-visible:outline-2 focus-visible:outline-ring active:cursor-grabbing [@media(pointer:coarse)]:size-11"
        {...attributes}
        {...listeners}
      >
        <GripVertical className="size-4" />
      </button>
      <WorkspaceAvatar name={workspace.name} avatarUrl={workspace.avatar_url} size="sm" />
      <span className="min-w-0 flex-1 truncate text-body">{workspace.name}</span>
      <Button
        variant="ghost"
        size="icon-sm"
        aria-label={pinLabel}
        title={pinLabel}
        aria-pressed={pinned}
        className={cn(pinned ? "text-foreground" : "text-muted-foreground")}
        onClick={onTogglePin}
      >
        {pinned ? <PinOff /> : <Pin />}
      </Button>
    </div>
  );
}
