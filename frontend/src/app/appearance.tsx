import { RadioGroup, RadioOption } from "@/ds/components/radio-group";
import { SettingsPage } from "@/ds/components/settings-page";
import { SettingsSection } from "@/ds/components/settings-section";
import { useTheme } from "@/ds/theme/theme-provider";
import type { ThemePreference } from "@/ds/theme/theme";
import { useTitle } from "./use-title";

/* Settings: appearance. The color theme is a per-browser preference,
 * stored locally and never sent to the server. */
export function AppearanceScreen() {
  useTitle("Appearance");
  const { theme, setTheme } = useTheme();

  return (
    <SettingsPage
      title="Appearance"
      description="How withHuman looks in this browser. The choice is saved on this device only."
    >
      <SettingsSection
        title="Theme"
        description="Pick which withHuman theme you like."
        inset="none"
      >
        <div className="p-2">
          <RadioGroup
            aria-label="Color theme"
            value={theme}
            onValueChange={(value) => setTheme(value as ThemePreference)}
          >
            <RadioOption value="system" label="System" description="Match the operating system." />
            <RadioOption value="light" label="Light" description="Porcelain canvas, onyx ink." />
            <RadioOption value="dark" label="Dark" description="For dim rooms and late reviews." />
          </RadioGroup>
        </div>
      </SettingsSection>
    </SettingsPage>
  );
}
