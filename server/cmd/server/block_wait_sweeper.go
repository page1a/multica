package main

import (
	"context"
	"log/slog"
	"time"

	"github.com/multica-ai/multica/server/internal/handler"
)

const blockWaitSweepInterval = time.Minute

func runBlockWaitSweeper(ctx context.Context, h *handler.Handler) {
	runPeriodicSweep(ctx, blockWaitSweepInterval, func() {
		n, err := h.SweepBlockWaits(ctx)
		if err != nil {
			slog.Warn("block wait sweeper failed", "error", err)
			return
		}
		if n > 0 {
			slog.Info("block wait sweeper woke stalled issues", "count", n)
		}
	})
}
