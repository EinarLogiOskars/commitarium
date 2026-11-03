// Stand-in for @tauri-apps/api/core in the docs screenshots: the app's
// commands answer from the demo world, never from a real Tauri backend.
import { invokeDemo } from "../world";

export async function invoke<T>(command: string, args?: Record<string, unknown>): Promise<T> {
  return invokeDemo(command, args) as T;
}
export const isTauri = () => false;
