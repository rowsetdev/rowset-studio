import { useQuery } from "@tanstack/react-query";
import type { ReactNode } from "react";
import { api } from "./api";

export interface Instance {
  mode: "personal" | "shared";
  desktop: boolean;
  duckdb?: boolean;
  engineCapabilities: Record<string, EngineCapabilities>;
}

export interface EngineCapabilities {
  transactions: boolean;
  explain: boolean;
  explainAnalyze: boolean;
  ddl: boolean;
  csvImport: boolean;
  csvExport: boolean;
  jsonExport: boolean;
  sqlExport: boolean;
  cqlExport: boolean;
  ssh: boolean;
  shared: boolean;
  multiNode: boolean;
  documentWrite: boolean;
  keyWrite: boolean;
}

const noCapabilities: EngineCapabilities = { transactions: false, explain: false, explainAnalyze: false, ddl: false, csvImport: false, csvExport: false, jsonExport: false, sqlExport: false, cqlExport: false, ssh: false, shared: false, multiNode: false, documentWrite: false, keyWrite: false };

export function useEngineCapabilities(engine?: string) {
  const instance = useInstance().data;
  return engine ? instance?.engineCapabilities?.[engine === "sqlserver" ? "mssql" : engine] ?? noCapabilities : noCapabilities;
}

export function useInstance() {
  return useQuery({ queryKey: ["instance"], queryFn: () => api<Instance>("/meta/instance") });
}

/** True on a shared, multi-user server. */
export function useShared() {
  return useInstance().data?.mode === "shared";
}

// Waits for the server profile before rendering pages that depend on it.
export function InstanceBoundary({ children }: { children: ReactNode }) {
  const { data, isError } = useInstance();
  if (isError) return <div role="alert" className="p-5">Unable to load Rowset. Reload to try again.</div>;
  if (!data) return <div className="p-5">Loading workspace…</div>;
  return children;
}
