/**
 * Project domain picker route (DENE-1451) — formSheet, multi-select: a tap
 * toggles a domain and the sheet stays open. 通用 clears them all.
 */
import { useLocalSearchParams } from "expo-router";
import { useQuery } from "@tanstack/react-query";
import { DomainPickerBody } from "@/components/domain/domain-picker-body";
import { domainListOptions } from "@/data/queries/domains";
import { projectDetailOptions } from "@/data/queries/projects";
import { useUpdateProject } from "@/data/mutations/projects";
import { useWorkspaceStore } from "@/data/workspace-store";
import { useT } from "@/lib/i18n";

export default function ProjectDomainPickerRoute() {
  const { t } = useT("common");
  const { id } = useLocalSearchParams<{ id: string }>();
  const wsId = useWorkspaceStore((s) => s.currentWorkspaceId);
  const { data: project } = useQuery(projectDetailOptions(wsId, id));
  const { data: domains = [] } = useQuery(domainListOptions(wsId));
  const updateProject = useUpdateProject(id);
  const selected = project?.domain_ids ?? [];

  return (
    <DomainPickerBody
      title={t("domain.label")}
      header={t("domain.project_header")}
      options={[
        { id: "", name: t("domain.generic") },
        ...domains.map((d) => ({ id: d.id, name: d.name })),
      ]}
      selected={selected}
      onPick={(picked) => {
        const next = !picked
          ? []
          : selected.includes(picked)
            ? selected.filter((s) => s !== picked)
            : [...selected, picked];
        updateProject.mutate({ domain_ids: next });
      }}
    />
  );
}
