import React from "react";
import ReactDOM from "react-dom/client";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { RouterProvider } from "react-router";
import { createRouter } from "./app/router";
import { registerExtensions, type StudioExtension } from "./app/extensions";
import { initAuth } from "./lib/auth";
import { ApiError } from "./lib/api";

export interface StudioOptions {
  /** Pages and page parts added to Studio. */
  extensions?: StudioExtension[];
}

/** Renders Rowset Studio into root. */
export function mountStudio(root: HTMLElement, options: StudioOptions = {}) {
  registerExtensions(options.extensions ?? []);
  // Restore any persisted JWT into the api layer before the first request.
  initAuth();
  const queryClient = new QueryClient({
    defaultOptions: {
      queries: {
        retry: (failureCount, error) => !(error instanceof ApiError && [401, 403, 429].includes(error.status)) && failureCount < 1,
        refetchOnWindowFocus: false,
      },
    },
  });
  ReactDOM.createRoot(root).render(
    <React.StrictMode>
      <QueryClientProvider client={queryClient}>
        <RouterProvider router={createRouter()} />
      </QueryClientProvider>
    </React.StrictMode>,
  );
}
