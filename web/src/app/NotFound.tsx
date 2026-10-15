import { Link } from "react-router";

import { buttonStyles } from "@/components/ui/button";

/** Renders the state for an unknown path under the application's base path. */
export function NotFound() {
  return (
    <div className="grid justify-items-start gap-md">
      <h1 className="font-heading text-h6 font-semibold">This page does not exist.</h1>
      <Link to="/" className={buttonStyles("secondary")}>
        Open Overview
      </Link>
    </div>
  );
}
