import type { ModuleKey, ModuleVisibility } from "../types";

/**
 * Whether the caller may enter this product area. Missing data defaults to
 * allowed so a slow first fetch does not hide the whole sidebar; a row that
 * came back with `allowed: false` is a definite no.
 *
 * Agents and squads are not modules — pass a key that is not in the list
 * and this returns true.
 */
export function canAccessModule(
  modules: ModuleVisibility[] | undefined,
  key: ModuleKey,
): boolean {
  if (!modules) return true;
  const row = modules.find((item) => item.key === key);
  if (!row) return true;
  return row.allowed !== false;
}

/** Map a sidebar/shortcut destination onto the module that gates it, or null. */
export function navItemModule(
  key: string,
): ModuleKey | null {
  switch (key) {
    case "myIssues":
    case "issues":
    case "goMyIssues":
    case "goIssues":
    case "createIssue":
      return "issues";
    case "projects":
    case "goProjects":
      return "projects";
    case "runtimes":
    case "goRuntimes":
      return "runtimes";
    default:
      return null;
  }
}
