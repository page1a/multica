/**
 * Pure picker body for domains (DENE-1451). A project takes several (tap
 * toggles, the sheet stays open), an issue one of its project's (tap picks and
 * the route closes). Mirrors `packages/views/domains/domain-select.tsx`.
 */
import { Pressable, ScrollView, View } from "react-native";
import { Ionicons } from "@expo/vector-icons";
import { useColorScheme } from "nativewind";
import { Text } from "@/components/ui/text";
import { THEME } from "@/lib/theme";

export interface DomainPickerOption {
  /** Domain id; "" is 通用 (no domain). */
  id: string;
  name: string;
}

interface Props {
  title: string;
  header: string;
  options: DomainPickerOption[];
  selected: string[];
  onPick: (id: string) => void;
}

export function DomainPickerBody({ title, header, options, selected, onPick }: Props) {
  const { colorScheme } = useColorScheme();
  const checkColor =
    colorScheme === "dark" ? THEME.dark.primary : THEME.light.primary;

  return (
    <ScrollView showsVerticalScrollIndicator={false}>
      <View className="px-4 pt-3 pb-2">
        <Text className="text-lg font-semibold text-foreground">{title}</Text>
        <Text className="text-sm text-muted-foreground">{header}</Text>
      </View>
      <View className="px-2">
        {options.map((o) => {
          const isSelected =
            o.id === "" ? selected.length === 0 : selected.includes(o.id);
          return (
            <Pressable
              key={o.id || "generic"}
              onPress={() => onPick(o.id)}
              className="flex-row items-center gap-3 rounded-lg px-3 py-3 active:bg-secondary"
            >
              <Text className="flex-1 text-base text-foreground">{o.name}</Text>
              {isSelected ? (
                <Ionicons name="checkmark" size={20} color={checkColor} />
              ) : null}
            </Pressable>
          );
        })}
      </View>
    </ScrollView>
  );
}
