import { createStream } from "@/stream/core";

import type { StreamMessage } from "@/stream/core";

const WORKER_NAME = "sortie-change-stream";

function connectFallback(onMessage: (m: StreamMessage) => void): () => void {
  let live = true;
  createStream((message) => {
    if (live) {
      onMessage(message);
    }
  }).open();
  return () => {
    live = false;
  };
}

function connectWorker(onMessage: (m: StreamMessage) => void): () => void {
  const worker = new SharedWorker(new URL("./worker.ts", import.meta.url), {
    name: WORKER_NAME,
  });
  let stopFallback: (() => void) | undefined;
  worker.port.onmessage = (event: MessageEvent<StreamMessage>) => onMessage(event.data);
  worker.onerror = () => {
    worker.port.close();
    stopFallback ??= connectFallback(onMessage);
  };
  worker.port.start();
  return () => {
    worker.port.onmessage = null;
    worker.port.close();
    stopFallback?.();
  };
}

/**
 * Connects the tab to the change stream and returns a disconnect. It calls
 * `onMessage` with the current status at once, then with every message. A
 * shared worker holds the single connection of the browser; where one cannot
 * be built the tab runs the stream itself.
 */
export function connectChangeStream(onMessage: (m: StreamMessage) => void): () => void {
  onMessage({ kind: "status", status: "connecting" });
  if (typeof SharedWorker !== "function") {
    return connectFallback(onMessage);
  }
  try {
    return connectWorker(onMessage);
  } catch {
    return connectFallback(onMessage);
  }
}
