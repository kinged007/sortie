import { useParams } from "react-router";

import { IssueDetail } from "@/components/IssueDetail";
import { useIssueSummary } from "@/components/use-issue-summary";

interface IssuePageProps {
  identifier: string;
}

function IssuePage({ identifier }: IssuePageProps) {
  const row = useIssueSummary(identifier);
  return <IssueDetail identifier={identifier} row={row} />;
}

/**
 * Renders the issue route: the peek's content in the page column. Showing the
 * issue reads its attention items and its ended entry.
 */
export function IssueScreen() {
  const { identifier } = useParams();
  return identifier === undefined ? undefined : <IssuePage identifier={identifier} />;
}
