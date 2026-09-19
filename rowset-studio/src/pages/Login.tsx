import { Icon } from "../components/Icon";
import RowsetLogo from "../components/RowsetLogo";
import { useInstance, useShared } from "../lib/instance";
import { extensions, type SignInCopy } from "../app/extensions";

const vendor = import.meta.env.VITE_ROWSET_VENDOR ?? "";

const personalCopy: SignInCopy = {
  subtitle: "Sign in with your Rowset account.",
  headline: "Your databases. Your workspace.",
  body: "Connect to your databases, save useful queries and work with policies you control.",
  footnote: "Encrypted credentials · Local history · Personal policies",
  capabilities: [
    { icon: "sql", label: "Your SQL workspace", note: "Browse schemas, run queries and manage transactions." },
    { icon: "shield", label: "Your policies", note: "Use default protections and create your own rules." },
    { icon: "lock", label: "Local storage", note: "Encrypted credentials and query history on your computer." },
  ],
};
function useSignIn() {
  const shared = useShared();
  const sharedCopy = extensions.find((item) => item.signIn)?.signIn;
  const productLabel = extensions.find((item) => item.productLabel)?.productLabel;
  return { copy: shared && sharedCopy ? sharedCopy : personalCopy, label: shared && productLabel ? productLabel : "Community" };
}

export default function Login() {
  const { copy } = useSignIn();
  const desktop = Boolean(useInstance().data?.desktop);
  const SignInForm = extensions.find((item) => item.signInForm)?.signInForm;

  // The desktop app signs in through its launcher, never with a password.
  if (desktop || !SignInForm) {
    return (
      <AuthShell title="Open Rowset Studio" subtitle="This session ended. Open Rowset Studio again to continue where you left off.">
        <ul className="space-y-2 text-[13px] leading-5 text-slate-600 dark:text-slate-300">
          <li>macOS: click <b>Rowset</b> in the menu bar, then <b>Open Rowset</b>, or open Rowset Studio from Applications.</li>
          <li>Windows: open <b>Rowset Studio</b> from the Start menu.</li>
          <li>Linux or a terminal: run <code>rowset</code>.</li>
        </ul>
      </AuthShell>
    );
  }

  return (
    <AuthShell title="Sign in" subtitle={copy.subtitle}>
      <SignInForm />
    </AuthShell>
  );
}

export function AuthShell({
  title,
  subtitle,
  children,
}: {
  title: string;
  subtitle?: string;
  children: React.ReactNode;
}) {
  const { copy, label } = useSignIn();
  return (
    <div className="grid min-h-screen bg-paper text-ink dark:bg-[#121317] dark:text-slate-100 lg:grid-cols-[1.05fr_1fr]">
      {/* Brand / value panel. Hidden on small screens. */}
      <aside className="rowset-auth-brand relative hidden flex-col justify-between overflow-hidden p-10 text-white lg:flex">
        <div className="flex items-center gap-2.5">
          <RowsetLogo size={36} className="rounded-[9px] ring-1 ring-white/25" />
          <div>
            <div className="text-[15px] font-semibold tracking-wide">Rowset Studio</div>
            <div className="text-[11px] tracking-wide text-white/60">{label}{vendor ? ` · by ${vendor}` : ""}</div>
          </div>
        </div>

        <div className="max-w-md">
          <h2 className="text-[26px] font-semibold leading-tight tracking-tight">{copy.headline}</h2>
          <p className="mt-3 text-[13px] leading-6 text-white/70">{copy.body}</p>

          <ul className="mt-8 space-y-4">
            {copy.capabilities.map((c) => (
              <li key={c.label} className="flex gap-3">
                <span className="mt-0.5 grid h-7 w-7 shrink-0 place-items-center rounded-md bg-white/10 text-brand-100 ring-1 ring-white/15">
                  <Icon name={c.icon} size={14} />
                </span>
                <div>
                  <div className="text-[13px] font-medium text-white">{c.label}</div>
                  <div className="text-[12px] leading-5 text-white/55">{c.note}</div>
                </div>
              </li>
            ))}
          </ul>
        </div>

        <div className="flex items-center gap-2 text-[11px] text-white/50">
          <Icon name="lock" size={12} />
          {copy.footnote}
        </div>
      </aside>

      {/* Form column */}
      <main className="flex items-center justify-center px-4 py-10">
        <div className="w-full max-w-[380px]">
          <div className="mb-6 flex items-center gap-2 lg:hidden">
            <RowsetLogo size={32} />
            <span className="text-[15px] font-semibold tracking-wide text-slate-900 dark:text-slate-100">Rowset Studio</span>
          </div>
          <h1 className="text-[20px] font-semibold tracking-tight text-slate-900 dark:text-slate-50">{title}</h1>
          {subtitle && <p className="mt-1.5 text-[13px] leading-5 text-slate-500 dark:text-slate-400">{subtitle}</p>}
          <div className="mt-6">{children}</div>
        </div>
      </main>
    </div>
  );
}
