// Stand-in for @tauri-apps/plugin-http: requests answer from the demo world.
import { fetchDemo } from "../world";

export async function fetch(input: string | URL | Request, init?: RequestInit): Promise<Response> {
  return fetchDemo(String(input instanceof Request ? input.url : input), init);
}
