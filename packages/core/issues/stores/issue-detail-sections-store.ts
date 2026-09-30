import { create } from "zustand";
import { createJSONStorage, persist } from "zustand/middleware";
import { defaultStorage } from "../../platform/storage";

/**
 * Which collapsible sections of the issue-detail sidebar are expanded.
 *
 * A personal layout preference, like the sub-issue row display: someone who
 * folds away "Details" wants it folded on every issue and after a reload, so
 * the flags live on `defaultStorage`, shared across issues and workspaces.
 * Before this store the flags were component state and a phone browser
 * reloading a discarded tab reopened every section.
 */
export const ISSUE_DETAIL_SECTIONS = [
  "properties",
  "parentIssue",
  "pullRequests",
  "details",
] as const;
export type IssueDetailSection = (typeof ISSUE_DETAIL_SECTIONS)[number];
export type IssueDetailSectionsOpen = Record<IssueDetailSection, boolean>;

export const DEFAULT_ISSUE_DETAIL_SECTIONS_OPEN: IssueDetailSectionsOpen = {
  properties: true,
  parentIssue: true,
  pullRequests: true,
  details: true,
};

interface IssueDetailSectionsStore {
  open: IssueDetailSectionsOpen;
  toggle: (section: IssueDetailSection) => void;
}

export const useIssueDetailSectionsStore = create<IssueDetailSectionsStore>()(
  persist(
    (set) => ({
      open: { ...DEFAULT_ISSUE_DETAIL_SECTIONS_OPEN },
      toggle: (section) =>
        set((s) => ({ open: { ...s.open, [section]: !s.open[section] } })),
    }),
    {
      name: "multica_issue_detail_sections",
      version: 1,
      storage: createJSONStorage(() => defaultStorage),
      partialize: (state) => ({ open: state.open }),
      // Deep-merge and type-check each flag: a section added in a later
      // release defaults to its own default instead of `undefined` (persist's
      // shallow merge would replace the whole object with the older one), and
      // a malformed value falls back rather than collapsing a section.
      merge: (persisted, current) => {
        const saved = ((persisted ?? {}) as { open?: Record<string, unknown> }).open ?? {};
        const open = { ...current.open };
        for (const section of ISSUE_DETAIL_SECTIONS) {
          const value = saved[section];
          if (typeof value === "boolean") open[section] = value;
        }
        return { ...current, open };
      },
    },
  ),
);
