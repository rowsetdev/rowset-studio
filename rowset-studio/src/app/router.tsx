import { lazy } from "react";
import { createBrowserRouter, Navigate, type RouteObject } from "react-router";
import AdminOnly from "../components/AdminOnly";
import AppErrorBoundary from "../components/AppErrorBoundary";
import ProtectedLayout from "../components/Layout";
import Login from "../pages/Login";
import Account from "../pages/Account";
import { useInstance } from "../lib/instance";
import { useQuery } from "@tanstack/react-query";
import { listConnections } from "../features/connections/api";
import { extensions } from "./extensions";

function WorkspaceHome() {
  const extensionHome = extensions.find((item) => item.homePath)?.homePath;
  const { data } = useInstance();
  const personal = !extensionHome || data?.mode !== "shared";
  const connections = useQuery({ queryKey: ["connections"], queryFn: listConnections, enabled: Boolean(data) && personal });
  if (!data) return null;
  if (!personal && extensionHome) return <Navigate to={extensionHome} replace />;
  if (connections.isPending) return <p className="p-6 text-sm">Opening your workspace…</p>;
  if (connections.isError) return <div className="p-6 text-sm">Could not load connections. <button className="underline" onClick={() => void connections.refetch()}>Retry</button></div>;
  return <Navigate to={connections.data.length ? "/editor" : "/connections"} replace />;
}

const ConnectionsPage = lazy(() => import("../features/connections/ConnectionsPage"));
const EditorPage = lazy(() => import("../features/editor/EditorPage"));
const PoliciesPage = lazy(() => import("../features/policies/PoliciesPage"));
const ActivityPage = lazy(() => import("../features/activity/ActivityPage"));
const NotebooksPage = lazy(() => import("../features/notebooks/NotebooksPage"));
const SchedulesPage = lazy(() => import("../features/schedules/SchedulesPage"));
const SchemaComparePage = lazy(() => import("../features/editor/SchemaComparePage"));
const ErDiagramPage = lazy(() => import("../features/diagram/ErDiagramPage"));

// Public auth routes plus the signed-in app shell; extensions add their pages
// under the same shell.
export function createRouter() {
  const extensionRoutes: RouteObject[] = extensions.flatMap((item) => item.routes ?? []);
  return createBrowserRouter([
  { path: "/login", element: <Login />, errorElement: <AppErrorBoundary /> },
  {
    element: <ProtectedLayout />,
    errorElement: <AppErrorBoundary />,
    children: [
      { index: true, element: <WorkspaceHome /> },
      { path: "account", element: <Account /> },
      { path: "connections", element: <ConnectionsPage /> },
      { path: "documents", element: <Navigate to="/editor" replace /> },
      { path: "editor", element: <EditorPage /> },
      { path: "activity", element: <ActivityPage /> },
      { path: "notebooks", element: <NotebooksPage /> },
      { path: "schedules", element: <SchedulesPage /> },
      { path: "diagram", element: <ErDiagramPage /> },
      { path: "schema-compare", element: <SchemaComparePage /> },
      { path: "history", element: <WorkspaceHome /> },
      {
        path: "policies",
        element: (
          <AdminOnly>
            <PoliciesPage />
          </AdminOnly>
        ),
      },
      ...extensionRoutes,
    ],
  },
]);
}
