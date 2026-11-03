// Stand-ins for the remaining Tauri modules the views import.
export async function openUrl(): Promise<void> {}
export async function open(): Promise<null> {
  return null;
}
export async function join(...parts: string[]): Promise<string> {
  return parts.join("/");
}
export async function isPermissionGranted(): Promise<boolean> {
  return true;
}
export async function requestPermission(): Promise<string> {
  return "granted";
}
export async function sendNotification(): Promise<void> {}
export async function getVersion(): Promise<string> {
  return "0.11.0";
}
