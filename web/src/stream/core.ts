/** The state of the change stream connection. */
export type StreamStatus = "connecting" | "open" | "closed";

/** A message from the stream to the tab: a connection status or a change signal carrying the state revision. */
export type StreamMessage =
  | { kind: "status"; status: StreamStatus }
  | { kind: "change"; revision: string };

const EVENTS_PATH = "/api/v1/events";
const INITIAL_DELAY_MS = 1_000;
const MAX_DELAY_MS = 30_000;
const IDLE_TIMEOUT_MS = 45_000;

/**
 * Creates the change stream: one `EventSource` that reopens with a doubling
 * delay and is dropped after 45 s without an event. `post` receives every
 * status change and change signal. The worker and the in-tab fallback both
 * run this module.
 */
export function createStream(post: (m: StreamMessage) => void): { open(): void } {
  let delayMs = INITIAL_DELAY_MS;
  let source: EventSource | undefined;
  let idleTimer: ReturnType<typeof setTimeout> | undefined;
  let reopenTimer: ReturnType<typeof setTimeout> | undefined;

  const postStatus = (status: StreamStatus) => post({ kind: "status", status });

  const stopIdle = () => {
    clearTimeout(idleTimer);
    idleTimer = undefined;
  };

  const reopenLater = () => {
    stopIdle();
    postStatus("closed");
    clearTimeout(reopenTimer);
    reopenTimer = setTimeout(open, delayMs);
    delayMs = Math.min(delayMs * 2, MAX_DELAY_MS);
  };

  const armIdle = (current: EventSource) => {
    stopIdle();
    idleTimer = setTimeout(() => {
      current.close();
      reopenLater();
    }, IDLE_TIMEOUT_MS);
  };

  function open(): void {
    source?.close();
    const current = new EventSource(EVENTS_PATH);
    source = current;
    postStatus("connecting");
    current.onopen = () => {
      delayMs = INITIAL_DELAY_MS;
      postStatus("open");
      armIdle(current);
    };
    current.addEventListener("change", (event) => {
      armIdle(current);
      post({ kind: "change", revision: (event as MessageEvent).lastEventId });
    });
    current.addEventListener("heartbeat", () => armIdle(current));
    current.onerror = () => {
      if (current.readyState === EventSource.CLOSED) {
        reopenLater();
        return;
      }
      postStatus("connecting");
    };
  }

  return { open };
}
