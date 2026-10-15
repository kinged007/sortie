import { useQueryClient } from "@tanstack/react-query";
import {
  createContext,
  use,
  useCallback,
  useEffect,
  useMemo,
  useRef,
  useState,
  useSyncExternalStore,
} from "react";

import { useStateQuery } from "@/api/queries";
import { now } from "@/lib/server-clock";
import { connectChangeStream } from "@/stream/client";

import type { Query, QueryClient } from "@tanstack/react-query";
import type { ReactNode } from "react";

import type { StreamMessage, StreamStatus } from "@/stream/core";

const STATE_KEY_ROOT = "state";

function floorOf(query: Query): number | undefined {
  const floor = query.meta?.refetchFloorMs;
  return typeof floor === "number" ? floor : undefined;
}

interface FloorGate {
  /** Refetches a floored query now, after its floor, or after its running fetch ends. */
  request: (query: Query) => void;
  /** Starts watching the cache; the returned function stops it and clears every timer and mark. */
  watch: () => () => void;
}

/**
 * Spaces the refetches of queries that carry `meta.refetchFloorMs`. A running
 * fetch is never canceled: a signal during it is remembered and the query is
 * checked again once the fetch ends, because its result can predate the signal.
 */
function createFloorGate(
  client: QueryClient,
  isPaused: () => boolean,
  holdChange: () => void,
): FloorGate {
  const timers = new Map<Query, ReturnType<typeof setTimeout>>();
  const deferred = new Set<Query>();

  function forget(query: Query): void {
    clearTimeout(timers.get(query));
    timers.delete(query);
    deferred.delete(query);
  }

  // A fresh query needs nothing: another path, such as a window focus, may have refetched it meanwhile.
  function recheck(query: Query): void {
    if (isPaused()) {
      holdChange();
      return;
    }
    if (query.isActive() && query.isStale() && document.visibilityState !== "hidden") {
      request(query);
    }
  }

  function request(query: Query): void {
    const floor = floorOf(query);
    if (floor === undefined) {
      return;
    }
    if (query.state.fetchStatus !== "idle") {
      deferred.add(query);
      return;
    }
    const due = Math.max(query.state.dataUpdatedAt, query.state.errorUpdatedAt) + floor;
    // The library stamps updates with Date.now(), so the floor reads the same clock.
    const wait = due - Date.now();
    if (wait <= 0) {
      void client.refetchQueries(
        { predicate: (candidate) => candidate === query },
        { cancelRefetch: false },
      );
    } else if (!timers.has(query)) {
      timers.set(
        query,
        setTimeout(() => {
          timers.delete(query);
          recheck(query);
        }, wait),
      );
    }
  }

  function watch(): () => void {
    const unsubscribe = client.getQueryCache().subscribe((event) => {
      if (event.type === "removed") {
        forget(event.query);
      } else if (
        event.type === "updated" &&
        deferred.has(event.query) &&
        event.query.state.fetchStatus === "idle"
      ) {
        deferred.delete(event.query);
        event.query.invalidate();
        recheck(event.query);
      }
    });
    return () => {
      unsubscribe();
      for (const query of [...timers.keys(), ...deferred]) {
        forget(query);
      }
    };
  }

  return { request, watch };
}

let streamStatus: StreamStatus = "connecting";
let streamStarted = false;
const statusListeners = new Set<() => void>();
const changeListeners = new Set<() => void>();

function handleStreamMessage(message: StreamMessage): void {
  if (message.kind === "status") {
    streamStatus = message.status;
    for (const listener of statusListeners) {
      listener();
    }
    return;
  }
  for (const listener of changeListeners) {
    listener();
  }
}

/**
 * Connects the tab to the change stream; later calls do nothing. Call it
 * before React renders so the first change signal is not missed.
 */
export function startLiveStream(): void {
  if (streamStarted) {
    return;
  }
  streamStarted = true;
  connectChangeStream(handleStreamMessage);
}

function subscribeStatus(listener: () => void): () => void {
  statusListeners.add(listener);
  return () => statusListeners.delete(listener);
}

function getStreamStatus(): StreamStatus {
  return streamStatus;
}

/** What the shell and the screens read about live updates. */
export interface LiveValue {
  paused: boolean;
  /** Flips the pause and returns the new value; resuming replays a change signal that arrived while paused. */
  togglePause: () => boolean;
  streamStatus: StreamStatus;
  /** The toast text once a state response carries another `version` than the first one; `null` before and after dismissal. */
  versionNotice: string | null;
  dismissVersionNotice: () => void;
}

const LiveContext = createContext<LiveValue | undefined>(undefined);

/** Provides the pause, the stream status, and the version notice, and refetches queries on every change signal. */
export function LiveProvider({ children }: LiveProviderProps) {
  const client = useQueryClient();
  const { data } = useStateQuery();
  const [paused, setPaused] = useState(false);
  const [versionNotice, setVersionNotice] = useState<string | null>(null);
  const pausedRef = useRef(false);
  const pendingChange = useRef(false);
  const floorGate = useRef<FloorGate | undefined>(undefined);
  const firstVersion = useRef<string | null>(null);
  const noticedVersion = useRef<string | null>(null);
  const status = useSyncExternalStore(subscribeStatus, getStreamStatus);

  const handleChange = useCallback(() => {
    if (pausedRef.current) {
      pendingChange.current = true;
      return;
    }
    void client.invalidateQueries({ refetchType: "none" });
    void client.refetchQueries({ queryKey: [STATE_KEY_ROOT] });
    if (document.visibilityState === "hidden") {
      return;
    }
    const others = { type: "active", stale: true } as const;
    void client.refetchQueries({
      ...others,
      predicate: (query) => query.queryKey[0] !== STATE_KEY_ROOT && floorOf(query) === undefined,
    });
    for (const query of client.getQueryCache().findAll({
      ...others,
      predicate: (candidate) => floorOf(candidate) !== undefined,
    })) {
      floorGate.current?.request(query);
    }
  }, [client]);

  useEffect(() => {
    const gate = createFloorGate(
      client,
      () => pausedRef.current,
      () => {
        pendingChange.current = true;
      },
    );
    const stopWatching = gate.watch();
    floorGate.current = gate;
    return () => {
      stopWatching();
      floorGate.current = undefined;
    };
  }, [client]);

  useEffect(() => {
    startLiveStream();
    changeListeners.add(handleChange);
    return () => {
      changeListeners.delete(handleChange);
    };
  }, [handleChange]);

  const version = data?.version;
  useEffect(() => {
    if (version === undefined) {
      return;
    }
    if (firstVersion.current === null) {
      firstVersion.current = version;
    } else if (version !== firstVersion.current && version !== noticedVersion.current) {
      noticedVersion.current = version;
      setVersionNotice(`Sortie ${version} is running. Reload to update the dashboard.`);
    }
  }, [version]);

  const togglePause = useCallback(() => {
    const next = !pausedRef.current;
    pausedRef.current = next;
    setPaused(next);
    if (!next && pendingChange.current) {
      pendingChange.current = false;
      handleChange();
    }
    return next;
  }, [handleChange]);

  const dismissVersionNotice = useCallback(() => setVersionNotice(null), []);

  const value = useMemo<LiveValue>(
    () => ({
      paused,
      togglePause,
      streamStatus: status,
      versionNotice,
      dismissVersionNotice,
    }),
    [paused, togglePause, status, versionNotice, dismissVersionNotice],
  );

  return <LiveContext value={value}>{children}</LiveContext>;
}

interface LiveProviderProps {
  children: ReactNode;
}

/**
 * Reads the live-update value.
 *
 * @throws When used outside `LiveProvider`.
 */
export function useLive(): LiveValue {
  const value = use(LiveContext);
  if (value === undefined) {
    throw new Error("useLive must be used inside LiveProvider");
  }
  return value;
}

/**
 * Returns the value as it was when `held` turned true for as long as `held`
 * stays true, else the live value. Pass one object bundling everything a view
 * renders so the whole view holds still together.
 */
export function useHold<T>(value: T, held: boolean): T {
  const [snapshot, setSnapshot] = useState<{ value: T } | null>(null);
  if (held && snapshot === null) {
    setSnapshot({ value });
  } else if (!held && snapshot !== null) {
    setSnapshot(null);
  }
  return held && snapshot !== null ? snapshot.value : value;
}

/** Returns the server-clock time in epoch milliseconds, refreshed every second and frozen while paused. */
export function useNow(): number {
  const { paused } = useLive();
  const [tick, setTick] = useState(now);

  useEffect(() => {
    if (paused) {
      return;
    }
    const id = setInterval(() => setTick(now()), 1_000);
    return () => clearInterval(id);
  }, [paused]);

  return tick;
}
