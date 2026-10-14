import { useEffect, useRef } from "react";
import { request } from "../api/client";
import { runPreviewCheck, runValidationJob } from "../ipc";
import type { AttentionItem } from "../api/types";

/** Runs pending validation jobs the coordinator marks `auto_run`: their work
 * order merges automatically once its gates pass, so nobody should have to
 * press Run. Each job is started once per app session; the job's own claim
 * keeps a manual run from racing it. */
export function useAutoValidation(items: AttentionItem[], refresh: () => void) {
  const started = useRef(new Set<string>());
  useEffect(() => {
    for (const item of items) {
      const jobId = item.validation_job_id;
      if (item.kind !== "validation_pending" || !item.auto_run || !jobId) continue;
      if (started.current.has(jobId)) continue;
      started.current.add(jobId);
      void runValidationJob(jobId)
        .catch(() => undefined)
        .finally(refresh);
    }
  }, [items, refresh]);
}

const ACCEPTANCE_POLL_MS = 10000;

/** Runs Commitarium's own runs of the reviewers' acceptance tests as they
 * appear. They inform the work order and need nobody's click. */
export function useAutoAcceptanceRuns(enabled: boolean) {
  const started = useRef(new Set<string>());
  useEffect(() => {
    if (!enabled) return;
    const poll = () =>
      request<{ jobs: { id: string }[] }>("/api/v1/acceptance-jobs/pending").then(
        ({ jobs }) => {
          for (const job of jobs) {
            if (started.current.has(job.id)) continue;
            started.current.add(job.id);
            void runValidationJob(job.id).catch(() => undefined);
          }
        },
        () => undefined,
      );
    void poll();
    const timer = setInterval(() => void poll(), ACCEPTANCE_POLL_MS);
    return () => clearInterval(timer);
  }, [enabled]);
}

const PREVIEW_CHECK_POLL_MS = 10000;

/** Runs Commitarium's preview checks as they appear (ADR-028): building and
 * starting a preview-setup work order's commit for real is what lets it
 * merge, and it needs nobody's click. The backend runs one at a time. */
export function useAutoPreviewChecks(enabled: boolean) {
  const started = useRef(new Set<string>());
  useEffect(() => {
    if (!enabled) return;
    const poll = () =>
      request<{ checks: { id: string }[] }>("/api/v1/preview-checks/pending").then(
        ({ checks }) => {
          for (const check of checks) {
            if (started.current.has(check.id)) continue;
            started.current.add(check.id);
            void runPreviewCheck(check.id).catch(() => undefined);
          }
        },
        () => undefined,
      );
    void poll();
    const timer = setInterval(() => void poll(), PREVIEW_CHECK_POLL_MS);
    return () => clearInterval(timer);
  }, [enabled]);
}
