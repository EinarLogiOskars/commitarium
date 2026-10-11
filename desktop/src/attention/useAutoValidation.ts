import { useEffect, useRef } from "react";
import { runValidationJob } from "../ipc";
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
