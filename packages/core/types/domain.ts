/**
 * Workspace domains (DENE-1451) — the one list projects, issues and
 * specialisations pick from. A project carries several, an issue works in one
 * of its project's, a specialisation is a base role plus one domain. No
 * domain means generic, which routing sends to the base role.
 */
export interface Domain {
  id: string;
  name: string;
  position: number;
  /** Projects carrying this domain. */
  project_count: number;
  /** Specialisations covering this domain. */
  agent_count: number;
}

export interface ListDomainsResponse {
  domains: Domain[];
}
