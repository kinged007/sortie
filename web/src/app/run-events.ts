import { useEffect, useState } from "react";

import { showRun, subscribeRunMessages } from "@/app/live";
import { emptyRunLog, mergeRun } from "@/stream/core";

import type { RunLog } from "@/stream/core";

interface HeldLog {
  dispatchId: string | null;
  log: RunLog;
}

/**
 * Holds the log of the run this tab shows. The tab asks the change stream for
 * the run while the document is visible and drops the request when it is
 * hidden, on page hide, and on unmount; the log survives a hidden spell and the
 * replay on return merges by `seq`.
 *
 * The tab has one run to show, so one component calls this at a time.
 *
 * @param dispatchId - The run to show, or null for none.
 */
export function useRunEvents(dispatchId: string | null): RunLog {
  const [held, setHeld] = useState<HeldLog>({ dispatchId, log: emptyRunLog() });
  if (held.dispatchId !== dispatchId) {
    setHeld({ dispatchId, log: emptyRunLog() });
  }

  useEffect(() => {
    if (dispatchId === null) {
      return;
    }
    return subscribeRunMessages((message) => {
      if (message.dispatchId !== dispatchId) {
        return;
      }
      setHeld((current) => {
        if (current.dispatchId !== dispatchId) {
          return current;
        }
        const log = mergeRun(current.log, message);
        return log === current.log ? current : { dispatchId, log };
      });
    });
  }, [dispatchId]);

  useEffect(() => {
    if (dispatchId === null) {
      return;
    }
    const handleVisibility = () =>
      showRun(document.visibilityState === "visible" ? dispatchId : null);
    const handlePageHide = () => showRun(null);
    handleVisibility();
    document.addEventListener("visibilitychange", handleVisibility);
    window.addEventListener("pagehide", handlePageHide);
    return () => {
      document.removeEventListener("visibilitychange", handleVisibility);
      window.removeEventListener("pagehide", handlePageHide);
      showRun(null);
    };
  }, [dispatchId]);

  return held.log;
}
