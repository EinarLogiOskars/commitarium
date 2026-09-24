import { useCallback, useEffect, useState } from "react";
import { isPermissionGranted, requestPermission } from "@tauri-apps/plugin-notification";
import { loadDesktopSettings, saveDesktopSettings, type DesktopSettings } from "../ipc";

const FALLBACK: DesktopSettings = {
  notifications_configured: false,
  notifications_enabled: false,
  notify_attention: true,
  notify_failures: true,
  notify_auto_merges: true,
  exit_behavior: "keep_running",
};

export interface DesktopSettingsState {
  settings: DesktopSettings;
  loaded: boolean;
  error: string | null;
  /** Persists a patch and adopts what the host echoes back. */
  save: (patch: Partial<DesktopSettings>) => Promise<void>;
  /**
   * The deliberate notification opt-in: ask the platform first, then record
   * that onboarding happened and enable delivery only if it was granted.
   */
  enableNotifications: () => Promise<boolean>;
}

/** Host-durable desktop preferences, shared by the notifier and the settings UI. */
export function useDesktopSettings(): DesktopSettingsState {
  const [settings, setSettings] = useState<DesktopSettings>(FALLBACK);
  const [loaded, setLoaded] = useState(false);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    loadDesktopSettings()
      .then(setSettings)
      .catch((e: unknown) => setError(String(e)))
      .finally(() => setLoaded(true));
  }, []);

  const save = useCallback(
    async (patch: Partial<DesktopSettings>) => {
      setError(null);
      try {
        setSettings(await saveDesktopSettings({ ...settings, ...patch }));
      } catch (e) {
        setError(String(e));
      }
    },
    [settings],
  );

  const enableNotifications = useCallback(async () => {
    const granted = (await isPermissionGranted()) || (await requestPermission()) === "granted";
    await save({ notifications_configured: true, notifications_enabled: granted });
    return granted;
  }, [save]);

  return { settings, loaded, error, save, enableNotifications };
}
