"use client";

import { useEffect, useLayoutEffect, useRef, useState, type ReactNode } from "react";
import { cn } from "@multica/ui/lib/utils";
import {
  CHAT_DETAIL_MIN_WIDTH,
  CHAT_LIST_MIN_WIDTH,
  chatListMaxWidth,
  clampChatListWidth,
  resolveChatListDrag,
  toggleChatListWidth,
  type ChatListToggle,
} from "@multica/core/chat/list-width";
import { useChatListWidthStore } from "@multica/core/chat/list-width-store";
import { useT } from "../../i18n";

/** The list pane's right border, which sits inside its width. */
const LIST_BORDER = 1;
const KEYBOARD_STEP = 16;
/** How long the width badge stays after a drag ends / after a double-click. */
const DRAG_FEEDBACK_LINGER_MS = 700;
const TOGGLE_FEEDBACK_LINGER_MS = 1100;

type Feedback =
  | { kind: "drag"; wanted: number; fitWidth: number | null }
  | { kind: "toggle"; reason: ChatListToggle["reason"] };

/**
 * The chat page's two panes with a freely draggable divider.
 *
 * The list may take everything except the conversation's minimum width, and
 * the width the person leaves it at is remembered in px. `fitContentWidth` is
 * how wide the list's content has to be for every project chip to fit on the
 * project bar: a drag snaps to it (dotted guide), a drag into the limit shows
 * why it stopped (dashed guide), and a double-click on the divider jumps to
 * the fit width and back to the default.
 */
export function ChatListSplit({
  list,
  detail,
  fitContentWidth,
}: {
  list: ReactNode;
  detail: ReactNode;
  fitContentWidth: number | null;
}) {
  const { t } = useT("chat");
  const storedWidth = useChatListWidthStore((s) => s.width);
  const setStoredWidth = useChatListWidthStore((s) => s.setWidth);

  const containerRef = useRef<HTMLDivElement | null>(null);
  const [containerWidth, setContainerWidth] = useState(0);
  useLayoutEffect(() => {
    const container = containerRef.current;
    if (!container) return;
    const measure = () => setContainerWidth(container.clientWidth);
    measure();
    if (typeof ResizeObserver === "undefined") return;
    const observer = new ResizeObserver(measure);
    observer.observe(container);
    return () => observer.disconnect();
  }, []);

  const fitWidth = fitContentWidth == null ? null : fitContentWidth + LIST_BORDER;
  const max = chatListMaxWidth(containerWidth);

  // A drag in progress: where it started, and where the pointer wants the edge.
  // The fit width is the one read when the drag began: the project bar
  // re-measures as the list resizes, and a snap target that could move under
  // the pointer would let the two chase each other.
  const dragOrigin = useRef<{ x: number; width: number; fitWidth: number | null } | null>(null);
  const [dragging, setDragging] = useState(false);
  const [feedback, setFeedback] = useState<Feedback | null>(null);
  const [animate, setAnimate] = useState(false);
  const feedbackTimer = useRef<ReturnType<typeof setTimeout> | null>(null);
  const clearFeedbackLater = (ms: number) => {
    if (feedbackTimer.current) clearTimeout(feedbackTimer.current);
    feedbackTimer.current = setTimeout(() => setFeedback(null), ms);
  };
  useEffect(
    () => () => {
      if (feedbackTimer.current) clearTimeout(feedbackTimer.current);
    },
    [],
  );

  const drag =
    feedback?.kind === "drag"
      ? resolveChatListDrag({ wanted: feedback.wanted, containerWidth, fitWidth: feedback.fitWidth })
      : null;
  // While dragging the pointer decides; otherwise the remembered width,
  // clamped to what this window can give.
  const width = dragging && drag ? drag.width : clampChatListWidth(storedWidth, containerWidth);

  const commit = (next: number) => {
    if (next !== storedWidth) setStoredWidth(next);
  };

  const toggle = () => {
    const next = toggleChatListWidth({ width, containerWidth, fitWidth });
    setAnimate(true);
    commit(next.width);
    setFeedback({ kind: "toggle", reason: next.reason });
    clearFeedbackLater(TOGGLE_FEEDBACK_LINGER_MS);
  };

  const onPointerDown = (event: React.PointerEvent<HTMLDivElement>) => {
    if (event.button !== 0) return;
    event.preventDefault();
    event.currentTarget.setPointerCapture?.(event.pointerId);
    dragOrigin.current = { x: event.clientX, width, fitWidth };
    if (feedbackTimer.current) clearTimeout(feedbackTimer.current);
    setAnimate(false);
    setDragging(true);
    setFeedback({ kind: "drag", wanted: width, fitWidth });
  };
  const onPointerMove = (event: React.PointerEvent<HTMLDivElement>) => {
    const origin = dragOrigin.current;
    if (!origin) return;
    setFeedback({
      kind: "drag",
      wanted: origin.width + event.clientX - origin.x,
      fitWidth: origin.fitWidth,
    });
  };
  const endDrag = () => {
    if (!dragOrigin.current) return;
    dragOrigin.current = null;
    setDragging(false);
    if (drag) commit(drag.width);
    clearFeedbackLater(DRAG_FEEDBACK_LINGER_MS);
  };

  const onKeyDown = (event: React.KeyboardEvent<HTMLDivElement>) => {
    const step =
      event.key === "ArrowLeft" ? -KEYBOARD_STEP : event.key === "ArrowRight" ? KEYBOARD_STEP : 0;
    if (step !== 0) {
      event.preventDefault();
      setAnimate(false);
      commit(clampChatListWidth(width + step, containerWidth));
    } else if (event.key === "Enter") {
      event.preventDefault();
      toggle();
    }
  };

  const badge = !feedback
    ? null
    : feedback.kind === "toggle"
      ? feedback.reason === "fit"
        ? t(($) => $.list_resize.width_at_fit, { width })
        : feedback.reason === "max"
          ? t(($) => $.list_resize.width_max_rest_in_more, { width })
          : t(($) => $.list_resize.width_default, { width })
      : drag?.atMax
        ? t(($) => $.list_resize.width_at_max, { width })
        : drag?.atFit
          ? t(($) => $.list_resize.width_at_fit, { width })
          : t(($) => $.list_resize.width, { width });

  return (
    <div ref={containerRef} className="relative flex min-h-0 flex-1" data-slot="chat-list-split">
      <div
        data-slot="chat-list-pane"
        style={{ width }}
        className={cn(
          "flex h-full shrink-0 flex-col border-r",
          animate && "transition-[width] duration-200 ease-out",
        )}
        onTransitionEnd={(event) => {
          if (event.target === event.currentTarget) setAnimate(false);
        }}
      >
        {list}
      </div>
      <div
        role="separator"
        aria-orientation="vertical"
        aria-label={t(($) => $.list_resize.label)}
        aria-valuenow={width}
        aria-valuemin={CHAT_LIST_MIN_WIDTH}
        aria-valuemax={Number.isFinite(max) ? max : undefined}
        tabIndex={0}
        data-dragging={dragging || undefined}
        onPointerDown={onPointerDown}
        onPointerMove={onPointerMove}
        onPointerUp={endDrag}
        onPointerCancel={endDrag}
        onDoubleClick={toggle}
        onKeyDown={onKeyDown}
        className={cn(
          "group/divider relative z-10 -mx-1 w-2 shrink-0 cursor-col-resize touch-none select-none focus-visible:outline-hidden",
          "before:absolute before:inset-y-0 before:left-1/2 before:w-px before:-translate-x-1/2 before:bg-transparent before:transition-colors",
          "hover:before:w-0.5 hover:before:bg-brand focus-visible:before:w-0.5 focus-visible:before:bg-brand data-dragging:before:w-0.5 data-dragging:before:bg-brand",
        )}
      >
        <span
          aria-hidden
          className="pointer-events-none absolute top-1/2 left-1/2 h-7 w-1.5 -translate-x-1/2 -translate-y-1/2 rounded-full bg-border opacity-0 transition-opacity group-hover/divider:opacity-100 group-data-dragging/divider:opacity-0"
        />
        <span
          aria-hidden
          className="pointer-events-none absolute top-1/2 left-3.5 -translate-y-1/2 rounded-md bg-foreground px-2 py-1 text-micro whitespace-nowrap text-background opacity-0 transition-opacity delay-500 group-hover/divider:opacity-100 group-data-dragging/divider:opacity-0 group-data-dragging/divider:delay-0"
        >
          {t(($) => $.list_resize.hint)}
        </span>
      </div>
      <div className="flex h-full min-h-0 min-w-0 flex-1 flex-col">{detail}</div>

      {badge && (
        <div
          role="status"
          data-slot="chat-list-width-badge"
          style={{ left: width + 10 }}
          className="pointer-events-none absolute top-3.5 z-20 rounded-md bg-foreground px-2 py-0.5 text-caption whitespace-nowrap text-background tabular-nums"
        >
          {badge}
        </div>
      )}
      {dragging && drag?.fitGuide != null && (
        <div
          data-slot="chat-list-fit-guide"
          style={{ left: drag.fitGuide }}
          className="pointer-events-none absolute inset-y-2 z-10 border-l-2 border-dotted border-brand/80"
        >
          <span className="absolute top-[30%] left-1 rounded-sm bg-brand px-1.5 py-px text-micro whitespace-nowrap text-brand-foreground">
            {t(($) => $.list_resize.fit_guide)}
          </span>
        </div>
      )}
      {dragging && drag?.atMax && Number.isFinite(max) && (
        <div
          data-slot="chat-list-limit-guide"
          style={{ left: max }}
          className="pointer-events-none absolute inset-y-2 z-10 border-l-2 border-dashed border-destructive/70"
        >
          <span className="absolute top-[40%] right-1 rounded-sm bg-destructive px-1.5 py-px text-micro whitespace-nowrap text-white">
            {t(($) => $.list_resize.limit_reason, { min: CHAT_DETAIL_MIN_WIDTH })}
          </span>
        </div>
      )}
    </div>
  );
}
