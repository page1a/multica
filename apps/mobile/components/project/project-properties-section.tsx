/**
 * Project properties section. Tappable rows for Status / Priority / Lead /
 * Domain.
 * Each row opens a picker sheet via the corresponding `onPress*` callback.
 *
 * Layout mirrors iOS Settings rows: label on left, current value on right
 * with a disclosure chevron, full-width separator below each row. Tapping
 * anywhere on the row triggers the picker.
 *
 * Lead supports both member and agent (Project.lead_type), resolved via
 * useActorLookup so it shares the same lookup with my-issues + issue detail.
 */
import { Pressable, View } from "react-native";
import { Ionicons } from "@expo/vector-icons";
import type { Project } from "@multica/core/types";
import { Text } from "@/components/ui/text";
import { ActorAvatar } from "@/components/ui/actor-avatar";
import { ProjectStatusIcon } from "@/components/ui/project-status-icon";
import { ProjectPriorityIcon } from "@/components/ui/project-priority-icon";
import {
  projectPriorityLabel,
  projectStatusLabel,
} from "@/lib/project-status";
import { useQuery } from "@tanstack/react-query";
import { useActorLookup } from "@/data/use-actor-name";
import { domainListOptions } from "@/data/queries/domains";
import { useWorkspaceStore } from "@/data/workspace-store";
import { useColorScheme } from "@/lib/use-color-scheme";
import { THEME } from "@/lib/theme";
import { useT } from "@/lib/i18n";

interface Props {
  project: Project;
  onPressStatus: () => void;
  onPressPriority: () => void;
  onPressLead: () => void;
  onPressDomain: () => void;
}

export function ProjectPropertiesSection({
  project,
  onPressStatus,
  onPressPriority,
  onPressLead,
  onPressDomain,
}: Props) {
  const { getName } = useActorLookup();
  const { t } = useT("projects");
  const wsId = useWorkspaceStore((s) => s.currentWorkspaceId);
  const { data: domains = [] } = useQuery(domainListOptions(wsId));
  const domainNames = domains
    .filter((d) => (project.domain_ids ?? []).includes(d.id))
    .map((d) => d.name);
  const leadName =
    project.lead_type && project.lead_id
      ? getName(project.lead_type, project.lead_id)
      : null;

  return (
    <View className="border-y border-border bg-background">
      <Row
        label={t("properties.status")}
        onPress={onPressStatus}
        left={<ProjectStatusIcon status={project.status} size={16} />}
        right={
          <Text className="text-sm text-foreground">
            {projectStatusLabel(project.status)}
          </Text>
        }
      />
      <Separator />
      <Row
        label={t("properties.priority")}
        onPress={onPressPriority}
        left={<ProjectPriorityIcon priority={project.priority} size={16} />}
        right={
          <Text className="text-sm text-foreground">
            {projectPriorityLabel(project.priority)}
          </Text>
        }
      />
      <Separator />
      <Row
        label={t("properties.lead")}
        onPress={onPressLead}
        left={
          leadName ? (
            <ActorAvatar
              type={project.lead_type}
              id={project.lead_id}
              size={20}
              showPresence
            />
          ) : (
            <PlaceholderAvatar />
          )
        }
        right={
          <Text
            className={
              leadName
                ? "text-sm text-foreground"
                : "text-sm text-muted-foreground"
            }
          >
            {leadName ?? t("properties.unassigned")}
          </Text>
        }
      />
      <Separator />
      <Row
        label={t("common:domain.label")}
        onPress={onPressDomain}
        left={null}
        right={
          <Text
            numberOfLines={1}
            className={
              domainNames.length > 0
                ? "flex-1 text-sm text-foreground"
                : "flex-1 text-sm text-muted-foreground"
            }
          >
            {domainNames.length > 0
              ? domainNames.join("、")
              : t("common:domain.generic")}
          </Text>
        }
      />
    </View>
  );
}

function Row({
  label,
  onPress,
  left,
  right,
}: {
  label: string;
  onPress: () => void;
  left: React.ReactNode;
  right: React.ReactNode;
}) {
  return (
    <Pressable
      onPress={onPress}
      className="flex-row items-center gap-3 px-4 py-3 active:bg-secondary"
    >
      <Text className="text-sm text-muted-foreground w-20">{label}</Text>
      <View className="flex-row items-center gap-2 flex-1">
        {left}
        {right}
      </View>
      <Chevron />
    </Pressable>
  );
}

function Separator() {
  return <View className="h-px bg-border ml-4" />;
}

function Chevron() {
  const { colorScheme } = useColorScheme();
  return (
    <Ionicons
      name="chevron-forward"
      size={14}
      color={THEME[colorScheme].mutedForeground}
    />
  );
}

function PlaceholderAvatar() {
  return (
    <View
      style={{ width: 20, height: 20, borderRadius: 10 }}
      className="border border-dashed border-muted-foreground/40"
    />
  );
}
