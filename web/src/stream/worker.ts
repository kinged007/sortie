import { createStream, isDispatchId } from "@/stream/core";

import type { ShowMessage, StreamMessage, StreamStatus } from "@/stream/core";

// Declared locally because the project compiles against the DOM library, not the worker one.
interface SharedWorkerScope {
  onconnect: ((event: MessageEvent) => void) | null;
}

const scope = self as unknown as SharedWorkerScope;
const ports = new Set<MessagePort>();
const shows = new Map<MessagePort, string | null>();
let order: string[] = [];
let status: StreamStatus = "connecting";

const stream = createStream((message: StreamMessage) => {
  if (message.kind === "status") {
    status = message.status;
  }
  for (const port of ports) {
    if (message.kind !== "run" || shows.get(port) === message.dispatchId) {
      port.postMessage(message);
    }
  }
});

function handleShow(port: MessagePort, message: ShowMessage): void {
  const id =
    message.dispatchId !== null && isDispatchId(message.dispatchId) ? message.dispatchId : null;
  shows.set(port, id);
  const shown = new Set(shows.values());
  order = order.filter((held) => shown.has(held));
  if (id !== null && !order.includes(id)) {
    order.push(id);
  }
  stream.watch(order);
  const held = id === null ? undefined : stream.snapshot(id);
  if (held !== undefined) {
    port.postMessage(held);
  }
}

scope.onconnect = (event) => {
  const [port] = event.ports;
  ports.add(port);
  shows.set(port, null);
  port.onmessage = (message: MessageEvent<ShowMessage>) => handleShow(port, message.data);
  port.postMessage({ kind: "status", status } satisfies StreamMessage);
  port.start();
};

stream.open();
