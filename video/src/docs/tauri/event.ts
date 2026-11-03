// Stand-in for @tauri-apps/api/event: nothing happens in a still.
export type UnlistenFn = () => void;
export async function listen(): Promise<UnlistenFn> {
  return () => {};
}
export async function emit(): Promise<void> {}
export async function once(): Promise<UnlistenFn> {
  return () => {};
}
