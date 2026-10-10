/**
 * Phone stand-in for a mermaid fence (DENE-1682): the groups of a 听汇报
 * flowchart as a small list with a count per group. See `mermaid-groups.ts`
 * for why it is not drawn.
 */
import { View } from "react-native";
import { Text } from "@/components/ui/text";
import { cn } from "@/lib/utils";
import type { MermaidGroup } from "./mermaid-groups";

// Colour only says state: waiting on you, running, finished.
function dotClass(title: string): string {
  if (title.includes("等你")) return "bg-brand";
  if (title.includes("做完")) return "bg-success";
  return "bg-muted-foreground/40";
}

export function MermaidSummary({ groups }: { groups: MermaidGroup[] }) {
  return (
    <View className="rounded-lg border border-border bg-card px-3 py-2 gap-2">
      {groups.map((group, i) => (
        <View key={i} className="gap-0.5">
          <View className="flex-row items-center gap-1.5">
            <View className={cn("size-1.5 rounded-full", dotClass(group.title))} />
            <Text className="text-xs font-medium text-foreground">
              {`${group.title} ${group.items.length}`}
            </Text>
          </View>
          {group.items.map((item, j) => (
            <Text key={j} className="pl-3 text-xs text-muted-foreground">
              {item}
            </Text>
          ))}
        </View>
      ))}
    </View>
  );
}
