import { useCallback } from "react";
import { useSearchParams } from "react-router";

const PEEK_PARAM = "peek";

/** The peek of the current location and the calls that change it. */
export interface PeekControls {
  /** The `issue_identifier` in the `peek` search parameter, or `null` while no peek is open. */
  identifier: string | null;
  /** Opens the peek and pushes a history entry, so Back closes it. */
  open: (identifier: string) => void;
  /** Moves an open peek to another issue without a history entry. */
  follow: (identifier: string) => void;
  /** Removes the parameter without a history entry. */
  close: () => void;
}

/** Reads and writes the `peek` search parameter, leaving every other parameter as it is. */
export function usePeek(): PeekControls {
  const [params, setParams] = useSearchParams();
  const value = params.get(PEEK_PARAM);

  const write = useCallback(
    (identifier: string | null, replace: boolean) => {
      setParams(
        (previous) => {
          const next = new URLSearchParams(previous);
          if (identifier === null) {
            next.delete(PEEK_PARAM);
          } else {
            next.set(PEEK_PARAM, identifier);
          }
          return next;
        },
        { replace },
      );
    },
    [setParams],
  );

  const open = useCallback((identifier: string) => write(identifier, false), [write]);
  const follow = useCallback((identifier: string) => write(identifier, true), [write]);
  const close = useCallback(() => write(null, true), [write]);

  return { identifier: value === null || value === "" ? null : value, open, follow, close };
}
