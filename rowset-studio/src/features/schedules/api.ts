import { api, apiResponse } from "../../lib/api";
import type { ScheduleSpec } from "./scheduleText";

export interface ScheduledRun {
  id: string;
  trigger: "schedule" | "manual";
  startedAt: string;
  finishedAt?: string | null;
  status: "running" | "success" | "error";
  rows: number;
  outputPath?: string | null;
  error?: string | null;
}

export interface ScheduledQuery {
  id: string;
  name: string;
  connectionId: string;
  database: string;
  sql: string;
  schedule: ScheduleSpec;
  outputDir: string;
  format: "csv" | "json";
  catchUp: boolean;
  enabled: boolean;
  nextRunAt: string | null;
  createdAt: string;
  updatedAt: string;
  running: boolean;
  lastRun?: ScheduledRun;
}

export type ScheduledInput = Pick<ScheduledQuery, "name" | "connectionId" | "database" | "sql" | "schedule" | "outputDir" | "format" | "catchUp" | "enabled">;

export function listSchedules() {
  return api<{ schedules: ScheduledQuery[] }>("/scheduled-queries").then((r) => r.schedules);
}

export function scheduleDefaults() {
  return api<{ outputDir: string; serverFolder?: boolean }>("/scheduled-queries/defaults");
}

/** Downloads the result file of a run (a server keeps it in its own folder). */
export async function downloadRunFile(queryId: string, run: ScheduledRun) {
  const response = await apiResponse(`/scheduled-queries/${queryId}/runs/${run.id}/file`);
  const url = URL.createObjectURL(await response.blob());
  const link = document.createElement("a");
  link.href = url;
  link.download = (run.outputPath ?? "result").split(/[\\/]/).pop() ?? "result";
  link.click();
  URL.revokeObjectURL(url);
}

export function createSchedule(input: ScheduledInput) {
  return api<ScheduledQuery>("/scheduled-queries", { method: "POST", body: JSON.stringify(input) });
}

export function updateSchedule(id: string, input: ScheduledInput) {
  return api<ScheduledQuery>(`/scheduled-queries/${id}`, { method: "PUT", body: JSON.stringify(input) });
}

export function deleteSchedule(id: string) {
  return api<void>(`/scheduled-queries/${id}`, { method: "DELETE" });
}

export function runSchedule(id: string) {
  return api<{ status: string }>(`/scheduled-queries/${id}/run`, { method: "POST" });
}

export function listRuns(id: string) {
  return api<{ runs: ScheduledRun[] }>(`/scheduled-queries/${id}/runs`).then((r) => r.runs);
}
