import { ToggleGroup, ToggleGroupItem } from "@/ds/components/toggle-group";
import { MonitorIcon, MoonIcon, SunIcon } from "@/ds/icons";
import { useTheme } from "./theme-provider";
import type { ThemePreference } from "./theme";

const OPTIONS: {
  value: ThemePreference;
  label: string;
  Icon: typeof SunIcon;
}[] = [
  { value: "light", label: "Light", Icon: SunIcon },
  { value: "system", label: "System", Icon: MonitorIcon },
  { value: "dark", label: "Dark", Icon: MoonIcon },
];

export function ThemeToggle({ className }: { className?: string }) {
  const { theme, setTheme } = useTheme();
  return (
    <ToggleGroup
      aria-label="Color theme"
      value={theme}
      onValueChange={(value) => setTheme(value as ThemePreference)}
      className={className}
    >
      {OPTIONS.map(({ value, label, Icon }) => (
        <ToggleGroupItem key={value} value={value} aria-label={label}>
          <Icon width={14} height={14} />
        </ToggleGroupItem>
      ))}
    </ToggleGroup>
  );
}
