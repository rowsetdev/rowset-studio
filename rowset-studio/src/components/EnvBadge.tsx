// Environment visual language: dev = orange, test/staging = yellow, prod = red.
// One source of truth so badges, connection rows, and the editor frame all agree.

type EnvKind = "dev" | "test" | "prod";

function kindOf(env: string): EnvKind {
  const e = env.toLowerCase();
  if (e.startsWith("prod") || e === "production" || e === "live") return "prod";
  if (e.startsWith("test") || e.startsWith("stag") || e.startsWith("pre") || e === "qa" || e === "uat") return "test";
  return "dev";
}

const badgeClass: Record<EnvKind, string> = {
  dev: "border-orange-200 bg-orange-50 text-orange-700 dark:border-orange-400/30 dark:bg-orange-500/10 dark:text-orange-300",
  test: "border-yellow-300 bg-yellow-50 text-yellow-800 dark:border-yellow-400/30 dark:bg-yellow-500/10 dark:text-yellow-300",
  prod: "border-rose-300 bg-rose-50 text-rose-700 dark:border-rose-400/30 dark:bg-rose-500/10 dark:text-rose-300",
};

// Left accent color for the editor frame + connection row rail.
export const envRail: Record<EnvKind, string> = {
  dev: "bg-orange-400",
  test: "bg-yellow-400",
  prod: "bg-rose-500",
};

// Border color for the SQL editor frame, tinted by environment. Marked
// important: Panel's base border-slate-200 otherwise wins or loses purely by
// stylesheet order (yellow happened to override it, rose happened not to).
export const envFrame: Record<EnvKind, string> = {
  dev: "!border-orange-300 dark:!border-orange-500/40",
  test: "!border-yellow-400 dark:!border-yellow-500/40",
  prod: "!border-rose-400 dark:!border-rose-500/50",
};

export function envKind(env: string): EnvKind {
  return kindOf(env);
}

const envText: Record<EnvKind, string> = { dev: "Dev", test: "Test", prod: "Prod" };

export function EnvBadge({ env }: { env: string }) {
  const kind = kindOf(env);
  return (
    <span className={`inline-flex h-5 items-center gap-1 rounded border px-1.5 text-[11px] font-medium ${badgeClass[kind]}`}>
      <span className={`h-1.5 w-1.5 rounded-full ${envRail[kind]}`} />
      {envText[kind]}
    </span>
  );
}
