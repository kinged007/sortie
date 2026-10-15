import { CSPProvider } from "@base-ui/react/csp-provider";
import { QueryClientProvider } from "@tanstack/react-query";
import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { RouterProvider } from "react-router";

import { queryClient } from "@/api/queries";
import { LiveProvider, startLiveStream } from "@/app/live";
import { getTheme, setTheme } from "@/lib/theme";
import { router } from "@/router";

import "./styles/index.css";

const root = document.getElementById("root");
if (root === null) {
  throw new Error("The #root element is missing from the document");
}

setTheme(getTheme());
startLiveStream();

createRoot(root).render(
  <StrictMode>
    <CSPProvider disableStyleElements>
      <QueryClientProvider client={queryClient}>
        <LiveProvider>
          <RouterProvider router={router} />
        </LiveProvider>
      </QueryClientProvider>
    </CSPProvider>
  </StrictMode>,
);
