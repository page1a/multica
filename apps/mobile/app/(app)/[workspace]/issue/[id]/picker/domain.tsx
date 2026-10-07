/**
 * Issue domain picker route (DENE-1451) — formSheet. The options are the
 * project's domains plus 通用; the server refuses anything else.
 */
import { useLocalSearchParams, router } from "expo-router";
import { useQuery } from "@tanstack/react-query";
import { DomainPickerBody } from "@/components/domain/domain-picker-body";
import { issueDetailOptions } from "@/data/queries/issues";
import { domainListOptions } from "@/data/queries/domains";
import { findProject, projectListOptions } from "@/data/queries/projects";
import { useUpdateIssue } from "@/data/mutations/issues";
import { useWorkspaceStore } from "@/data/workspace-store";
import { useT } from "@/lib/i18n";

export default function IssueDomainPickerRoute() {
  const { t } = useT("common");
  const { id } = useLocalSearchParams<{ id: string }>();
  const wsId = useWorkspaceStore((s) => s.currentWorkspaceId);
  const { data: issue } = useQuery(issueDetailOptions(wsId, id));
  const { data: projects = [] } = useQuery(projectListOptions(wsId));
  const { data: domains = [] } = useQuery(domainListOptions(wsId));
  const updateIssue = useUpdateIssue(id);

  const project = findProject(projects, issue?.project_id ?? null);
  const projectDomainIds = project?.domain_ids ?? [];
  const options = [
    { id: "", name: t("domain.generic") },
    ...domains
      .filter((d) => projectDomainIds.includes(d.id))
      .map((d) => ({ id: d.id, name: d.name })),
  ];

  return (
    <DomainPickerBody
      title={t("domain.label")}
      header={t("domain.issue_header")}
      options={options}
      selected={issue?.domain_id ? [issue.domain_id] : []}
      onPick={(next) => {
        updateIssue.mutate({ domain_id: next || null });
        router.back();
      }}
    />
  );
}
