// Typed wrappers around the explicit Rust command set. This is the only
// surface the UI uses to reach privileged host operations; it mirrors the
// commands registered in src-tauri/src/lib.rs.

import { invoke } from "@tauri-apps/api/core";
import { openUrl } from "@tauri-apps/plugin-opener";

export interface DockerProbe {
  docker_installed: boolean;
  docker_running: boolean;
  compose_available: boolean;
  docker_version: string | null;
  compose_version: string | null;
  install_url: string;
}

export interface ServiceStatus {
  service: string;
  state: string;
  health: string | null;
  status: string;
}

export const dockerProbe = (): Promise<DockerProbe> => invoke("docker_probe");

export const stackUp = (): Promise<void> => invoke("stack_up");
export const stackDown = (): Promise<void> => invoke("stack_down");
export const stackUpdate = (): Promise<void> => invoke("stack_update");
export const stackStatus = (): Promise<ServiceStatus[]> => invoke("stack_status");

export const loadUiState = <T = unknown>(): Promise<T | null> => invoke("load_ui_state");
export const saveUiState = (state: unknown): Promise<void> =>
  invoke("save_ui_state", { state });

// Open a URL in the user's default browser (used for the Docker install link).
export const openExternal = (url: string): Promise<void> => openUrl(url);
