import type { ComponentType } from "react";
import type { RouteObject } from "react-router";
import type { IconName } from "../components/Icon";
import type { Connection } from "../features/connections/api";
import type { QueryResult } from "../features/editor/api";
import { useShared } from "../lib/instance";

export interface NavItem {
  to: string;
  label: string;
  icon: IconName;
  end?: boolean;
  adminOnly?: boolean;
}

export interface NavGroup {
  title: string;
  adminOnly?: boolean;
  items: NavItem[];
}

/** The statement the editor ran when a policy refused it. */
export interface DenialContext {
  connectionId: string;
  sql: string;
  onMessage: (message: string, isError: boolean) => void;
}

export interface SignInCopy {
  subtitle: string;
  headline: string;
  body: string;
  footnote: string;
  capabilities: { icon: IconName; label: string; note: string }[];
}

export interface ConnectionFieldProps {
  connection?: Connection;
  name: string;
  values: Record<string, unknown>;
  onChange: (key: string, value: unknown) => void;
}

export interface PolicyTemplate {
  kind: string;
  label: string;
  configLabel: string;
  hint: string;
  /** The config names a statement kind. */
  statement?: boolean;
}

export interface PolicyRole {
  id: string;
  name: string;
}

export interface PolicyAction {
  value: string;
  label: string;
  icon: IconName;
  dot: string;
  tile: string;
  matches: (policyKey: string) => boolean;
}

// A Studio extension adds pages and page parts to the app shell. Extensions
// are passed to mountStudio (and every module in ./extensions is loaded at
// build time). Page parts apply on shared servers only.
export interface StudioExtension {
  /** Routes mounted under the signed-in app shell. */
  routes?: RouteObject[];
  /** Navigation that replaces the default personal navigation when present. */
  navGroups?: NavGroup[];
  /** Small components rendered in the sidebar footer. */
  sidebarWidgets?: ComponentType<{ collapsed: boolean }>[];
  /** Landing page for the index route. */
  homePath?: string;
  /** Product line shown under the Studio name. */
  productLabel?: string;
  /** Sign-in panel copy. */
  signIn?: SignInCopy;
  /** The sign-in form of a server that is not a desktop workspace. */
  signInForm?: ComponentType;
  /** Notices shown above every page of the signed-in shell. */
  notices?: ComponentType[];
  /** Extra sections of the Account page. */
  accountSections?: ComponentType[];
  /** Actions offered with a policy denial in the editor. */
  denialActions?: ComponentType<{ error: unknown; context: DenialContext }>[];
  /** Badges describing a result in the editor status bar. */
  resultBadges?: ComponentType<{ result: QueryResult }>[];
  /** Marker rendered next to a result column name. */
  columnMark?: ComponentType<{ result: QueryResult; column: string }>;
  /** Extra inputs in the connection form. */
  connectionFields?: ComponentType<ConnectionFieldProps>[];
  /** Replaces the address column of the connection list. */
  connectionAddress?: { title: string; cell: ComponentType<{ connection: Connection; isAdmin: boolean }> };
  /** Extra actions for each connection in the list. */
  connectionActions?: ComponentType<{ connection: Connection }>[];
  connectionsSubtitle?: string;
  /** Additional custom policy templates. */
  policyTemplates?: PolicyTemplate[];
  /** Additional policy action groups. */
  policyActions?: PolicyAction[];
  /** Roles a policy can be scoped to. */
  policyRoles?: () => Promise<PolicyRole[]>;
}

const modules = import.meta.glob<{ default: StudioExtension }>("./extensions/*.tsx", { eager: true });

export const extensions: StudioExtension[] = Object.values(modules).map((module) => module.default);

/** Adds extensions; call before the app renders (mountStudio does). */
export function registerExtensions(list: StudioExtension[]) {
  extensions.push(...list);
}

/** Extensions whose page parts apply to this server. */
export function useActiveExtensions(): StudioExtension[] {
  return useShared() ? extensions : [];
}
