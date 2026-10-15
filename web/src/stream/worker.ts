import { createStream } from "@/stream/core";

import type { StreamMessage, StreamStatus } from "@/stream/core";

// Declared locally because the project compiles against the DOM library, not the worker one.
interface SharedWorkerScope {
  onconnect: ((event: MessageEvent) => void) | null;
}

const scope = self as unknown as SharedWorkerScope;
const ports = new Set<MessagePort>();
let status: StreamStatus = "connecting";

const stream = createStream((message: StreamMessage) => {
  if (message.kind === "status") {
    status = message.status;
  }
  for (const port of ports) {
    port.postMessage(message);
  }
});

scope.onconnect = (event) => {
  const [port] = event.ports;
  ports.add(port);
  port.postMessage({ kind: "status", status } satisfies StreamMessage);
  port.start();
};

stream.open();
