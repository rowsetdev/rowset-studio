// The public entry of the Studio package: mountStudio and what extensions
// build their pages with. Everything else is internal to Studio.
export { mountStudio, type StudioOptions } from "./studio";
export type {
  StudioExtension,
  NavGroup,
  NavItem,
  DenialContext,
  SignInCopy,
  ConnectionFieldProps,
  PolicyTemplate,
  PolicyAction,
} from "./app/extensions";

// Components.
export { Badge, Button, ErrorText, Field, Input, Modal, PageHeader, Panel, SegTabs, Select, Textarea } from "./components/ui";
export { Icon, type IconName } from "./components/Icon";
export { default as EngineLogo, engineLabel } from "./components/EngineLogo";
export { EnvBadge } from "./components/EnvBadge";
export { default as AdminOnly } from "./components/AdminOnly";

// Server access and session.
export { api, apiResponse, ApiError, type ApiErrorBody } from "./lib/api";
export { useAuth, startSession, type AuthResponse, type Identity } from "./lib/auth";
export { useInstance, useShared, type Instance } from "./lib/instance";

// Data.
export { listConnections, type Connection, type Engine } from "./features/connections/api";
export { getSchema, listDatabases, type QueryResult } from "./features/editor/api";
