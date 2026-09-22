// @vitest-environment node
import { describe, expect, it } from "vitest";
import type { TaskUsage } from "@multica/core/types";
import { summarizeTaskUsage } from "../../runtimes/utils";
import {
  defaultSelectedRunId,
  formatMillis,
  runCacheHitRate,
  runCostComposition,
  runMetadata,
  runShareOfIssue,
} from "./run-cost";

// Canonical matrix for the per-run Token cost arithmetic. The component suite
// (run-cost-breakdown.test.tsx) keeps the happy path and the wiring and does
// not re-run these cases through a DOM mount.

function usage(overrides: Partial<TaskUsage> = {}): TaskUsage {
  return {
    provider: "anthropic",
    model: "claude-opus-5",
    input_tokens: 0,
    output_tokens: 0,
    cache_read_tokens: 0,
    cache_write_tokens: 0,
    ...overrides,
  };
}

describe("runCostComposition", () => {
  it("returns null for a run with no usage recorded", () => {
    // Not zero: an un-metered run was not free, and a 0-cost composition would
    // draw an empty bar that reads as "this run spent nothing".
    expect(runCostComposition(undefined)).toBeNull();
    expect(runCostComposition([])).toBeNull();
  });

  it("splits tokens into the four billing segments", () => {
    const composition = runCostComposition([
      usage({
        input_tokens: 1_000,
        output_tokens: 2_000,
        cache_read_tokens: 3_000,
        cache_write_tokens: 4_000,
      }),
    ]);

    expect(composition?.segments.map((s) => [s.key, s.tokens])).toEqual([
      ["input", 1_000],
      ["cacheWrite", 4_000],
      ["cacheRead", 3_000],
      ["output", 2_000],
    ]);
    expect(composition?.tokens).toBe(10_000);
  });

  it("names the segment with the most tokens", () => {
    const composition = runCostComposition([
      usage({ input_tokens: 10, cache_read_tokens: 900, output_tokens: 90 }),
    ]);
    expect(composition?.largest.key).toBe("cacheRead");
    expect(composition?.largest.tokenShare).toBeCloseTo(0.9, 5);
  });

  it("keeps the declaration order on a tie instead of flipping between renders", () => {
    const composition = runCostComposition([
      usage({ input_tokens: 500, output_tokens: 500 }),
    ]);
    expect(composition?.largest.key).toBe("input");
  });

  it("sums the segment costs to the same total summarizeTaskUsage prices", () => {
    // The panel prints per-segment costs beside a headline the table also
    // prints. Two accounting paths that drift by a cent make the view useless
    // at exactly the job it exists for.
    const rows = [
      usage({
        input_tokens: 120_000,
        output_tokens: 8_000,
        cache_read_tokens: 2_000_000,
        cache_write_tokens: 40_000,
      }),
      usage({
        model: "claude-haiku-4-5-20251001",
        input_tokens: 5_000,
        output_tokens: 1_000,
      }),
    ];
    const composition = runCostComposition(rows)!;
    expect(composition.cost).toBeCloseTo(summarizeTaskUsage(rows)!.cost, 10);
  });

  it("keeps a provider-priced row's money in the total when no rate table covers it", () => {
    // cost_usd_ticks is 1e-10 USD. An unknown model has no rates to split by,
    // so estimateCostBreakdown lands the whole charge in one bucket rather
    // than dropping it — the bar must still add up to the headline.
    const rows = [
      usage({ model: "some-future-model", provider: "mystery", cost_usd_ticks: 25_000_000_000 }),
    ];
    const composition = runCostComposition(rows)!;
    expect(composition.cost).toBeCloseTo(2.5, 6);
  });

  it("reports zero shares rather than dividing by zero on an empty-but-present row", () => {
    const composition = runCostComposition([usage()])!;
    expect(composition.tokens).toBe(0);
    expect(composition.segments.every((s) => s.tokenShare === 0)).toBe(true);
    expect(composition.segments.every((s) => s.costShare === 0)).toBe(true);
  });
});

describe("runCacheHitRate", () => {
  it("floors rather than rounding up to a claim of 100%", () => {
    // 99.55% — rounding prints "100%", which asserts not one fresh token was
    // read on a run that plainly read some.
    expect(runCacheHitRate([usage({ input_tokens: 573_500, cache_read_tokens: 126_000_000 })])).toBe(99);
  });

  it("still says 100 when it really is 100", () => {
    expect(runCacheHitRate([usage({ cache_read_tokens: 1_000 })])).toBe(100);
  });

  it("is null, not 0, when the run sent no prompt tokens", () => {
    // 0% would assert a total cache miss on a run that never asked.
    expect(runCacheHitRate([usage({ output_tokens: 10 })])).toBeNull();
  });

  it("is null for a run with no usage at all", () => {
    expect(runCacheHitRate(undefined)).toBeNull();
  });
});

describe("runShareOfIssue", () => {
  it("is the run's money over the issue's money", () => {
    expect(runShareOfIssue(2.5, 10)).toBeCloseTo(0.25, 10);
  });

  it("is null, not 0, when the issue total is zero", () => {
    expect(runShareOfIssue(0, 0)).toBeNull();
  });
});

describe("runMetadata", () => {
  it("does not sum run-level fields across a run's model slices", () => {
    // The daemon writes the same run-level values onto every (provider, model)
    // row, so a 12-turn run that spilled onto a second model must stay 12.
    const meta = runMetadata([
      usage({ num_turns: 12, resumed: true, total_ms: 90_000 }),
      usage({ model: "claude-haiku-4-5-20251001", num_turns: 12, resumed: true, total_ms: 90_000 }),
    ]);
    expect(meta.numTurns).toBe(12);
    expect(meta.totalMs).toBe(90_000);
    expect(meta.resumed).toBe(true);
  });

  it("treats a zero turn count as unreported", () => {
    // NOT NULL DEFAULT 0 server-side: a pre-DENE-666 row and a turn-less run
    // arrive identically, and "0 turns" would be a missing figure dressed up
    // as a measurement.
    expect(runMetadata([usage({ num_turns: 0 })]).numTurns).toBeUndefined();
  });

  it("keeps a reported zero duration, which is a measurement", () => {
    expect(runMetadata([usage({ queue_to_claim_ms: 0 })]).queueToClaimMs).toBe(0);
  });

  it("keeps a reported false for resumed, which means cold start", () => {
    expect(runMetadata([usage({ resumed: false })]).resumed).toBe(false);
  });

  it("reads an absent resumed as cold start when the run reported other metadata", () => {
    // The wire shape the server actually sends: `resumed` is `omitempty`, so
    // a cold start is an absent field, never `false`.
    expect(runMetadata([usage({ num_turns: 7, session_id: "s-1" })]).resumed).toBe(false);
  });

  it("returns an empty record for a pre-metadata run", () => {
    expect(runMetadata([usage()])).toEqual({});
    expect(runMetadata(undefined)).toEqual({});
  });
});

describe("defaultSelectedRunId", () => {
  it("opens on the most expensive run, not the newest", () => {
    const tasks = [
      { id: "cheap", usage: [usage({ output_tokens: 10 })] },
      { id: "dear", usage: [usage({ output_tokens: 1_000_000 })] },
    ];
    expect(defaultSelectedRunId(tasks)).toBe("dear");
  });

  it("skips runs with no usage recorded", () => {
    expect(
      defaultSelectedRunId([
        { id: "unmetered" },
        { id: "metered", usage: [usage({ output_tokens: 1 })] },
      ]),
    ).toBe("metered");
  });

  it("is null when nothing is metered", () => {
    expect(defaultSelectedRunId([{ id: "a" }, { id: "b" }])).toBeNull();
  });
});

describe("formatMillis", () => {
  it("keeps sub-second phases legible instead of collapsing them to 0s", () => {
    // Queue and prepare marks are routinely under a second; second resolution
    // would print "0s" and read as "not measured".
    expect(formatMillis(340)).toBe("340ms");
  });

  it("formats seconds, minutes and hours", () => {
    expect(formatMillis(4_500)).toBe("4.5s");
    expect(formatMillis(125_000)).toBe("2m 5s");
    expect(formatMillis(3_900_000)).toBe("1h 5m");
  });

  it("is null for an absent or nonsensical figure", () => {
    expect(formatMillis(undefined)).toBeNull();
    expect(formatMillis(-1)).toBeNull();
    expect(formatMillis(Number.NaN)).toBeNull();
  });
});
