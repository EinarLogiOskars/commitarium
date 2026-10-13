import { request } from "./client";

// The app marketplace (MARKETPLACE_PLAN.md). The coordinator talks to it;
// the password is used once to sign in and is not kept.

export interface MarketplaceAccount {
  username: string;
  admin: boolean;
}

export interface MarketplaceStatus {
  url: string;
  account: MarketplaceAccount | null;
}

export interface PendingMarketplaceUser {
  username: string;
  email: string;
  requested_at: string;
}

export const getMarketplace = () => request<MarketplaceStatus>("/api/v1/marketplace");

export const signUpToMarketplace = (username: string, email: string, password: string) =>
  request<{ status: string }>("/api/v1/marketplace/sign-up", {
    method: "POST",
    body: { username, email, password },
  });

export const signInToMarketplace = (username: string, password: string) =>
  request<MarketplaceStatus>("/api/v1/marketplace/sign-in", {
    method: "POST",
    body: { username, password },
  });

export const signOutOfMarketplace = () =>
  request<void>("/api/v1/marketplace/sign-out", { method: "POST" });

export const listPendingMarketplaceUsers = () =>
  request<{ users: PendingMarketplaceUser[] }>("/api/v1/marketplace/pending-users").then(
    (r) => r.users,
  );

export const approveMarketplaceUser = (username: string) =>
  request<void>(`/api/v1/marketplace/pending-users/${encodeURIComponent(username)}/approve`, {
    method: "POST",
  });

export const declineMarketplaceUser = (username: string) =>
  request<void>(`/api/v1/marketplace/pending-users/${encodeURIComponent(username)}/decline`, {
    method: "POST",
  });
