import { useState } from "react";
import { useAuth } from "../lib/auth";
import { useInstance } from "../lib/instance";
import { extensions } from "../app/extensions";
import { PageHeader, Panel } from "../components/ui";
import { Icon, type IconName } from "../components/Icon";
import { rowBackupEnabled, setRowBackupEnabled } from "../lib/preferences";
import AiSettingsPanel from "../features/ai/AiSettingsPanel";
import SlackSettingsPanel from "../features/slack/SlackSettingsPanel";

const appVersion = import.meta.env.VITE_ROWSET_VERSION || "dev";

export default function Account() {
  const user = useAuth((s) => s.user);
  const { data: instance } = useInstance();
  const shared = instance?.mode === "shared";
  const desktop = Boolean(instance?.desktop);
  const [rowBackup, setRowBackup] = useState(rowBackupEnabled);
  const sections = extensions.flatMap((item) => item.accountSections ?? []);

  return (
    <div className="max-w-3xl space-y-5">
      <PageHeader icon="key" title="Account" subtitle="Your sign-in and this Rowset Studio installation." />

      <Panel className="p-4">
        <div className="flex items-center gap-3">
          <span className="grid h-10 w-10 shrink-0 place-items-center rounded-full bg-slate-100 text-[15px] font-semibold uppercase text-slate-600 dark:bg-slate-800 dark:text-slate-300">
            {shared ? (user?.email ?? "?").slice(0, 1) : <Icon name="key" size={16} />}
          </span>
          <div className="min-w-0">
            <div className="truncate text-[14px] font-medium text-slate-900 dark:text-slate-100">{shared ? user?.email : "Owner of this workspace"}</div>
            <div className="text-[12px] text-slate-500 dark:text-slate-400">{shared ? `Role: ${user?.role ?? "-"}` : desktop ? "Signed in automatically on this computer" : "Personal workspace"}</div>
          </div>
        </div>
        <dl className="mt-4 grid gap-3 border-t border-slate-100 pt-4 text-[13px] sm:grid-cols-3 dark:border-slate-800">
          <Detail icon="database" label="Workspace" value={shared ? "Shared server" : instance?.desktop ? "This computer" : "Personal"} />
          <Detail icon="lock" label="Stored data" value="Encrypted locally" />
          <Detail icon="activity" label="Version" value={`Rowset ${appVersion}`} />
        </dl>
      </Panel>

      {!shared && <Panel className="p-4">
        <div className="flex items-start justify-between gap-4">
          <div>
            <h2 className="text-[14px] font-semibold text-slate-900 dark:text-slate-100">Back up rows before UPDATE and DELETE</h2>
            <p className="mt-1 max-w-xl text-[12px] text-slate-500 dark:text-slate-400">
              Before an UPDATE or DELETE on one table with a WHERE clause, Rowset first reads the rows that clause matches and saves them, so you can put them back from Activity → Row backups.
              That read stops at 10,001 rows: if it gets that many, or the UPDATE is on a table without a primary key, the statement does not run and Rowset asks whether to run it without a backup.
            </p>
          </div>
          <button
            type="button"
            role="switch"
            aria-checked={rowBackup}
            aria-label="Back up rows before UPDATE and DELETE"
            onClick={() => { setRowBackupEnabled(!rowBackup); setRowBackup(!rowBackup); }}
            className={`relative mt-0.5 h-5 w-9 shrink-0 rounded-full transition-colors ${rowBackup ? "bg-emerald-500" : "bg-slate-300 dark:bg-slate-700"}`}
          >
            <span className={`absolute top-0.5 h-4 w-4 rounded-full bg-white shadow transition-all ${rowBackup ? "left-[18px]" : "left-0.5"}`} />
          </button>
        </div>
      </Panel>}

      {!shared && <AiSettingsPanel />}

      {!shared && <SlackSettingsPanel />}

      {sections.map((Section, index) => <Section key={index} />)}
    </div>
  );
}

function Detail({ icon, label, value }: { icon: IconName; label: string; value: string }) {
  return (
    <div className="flex items-start gap-2">
      <Icon name={icon} size={14} className="mt-0.5 text-slate-400" />
      <div>
        <dt className="text-[11px] font-medium tracking-wide text-slate-400">{label}</dt>
        <dd className="text-slate-700 dark:text-slate-200">{value}</dd>
      </div>
    </div>
  );
}
