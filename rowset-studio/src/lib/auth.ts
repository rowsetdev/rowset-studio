// Auth state. The short-lived access token lives only in memory (never in
// localStorage, so XSS cannot lift a credential from storage); sessions
// survive reloads via an HttpOnly refresh cookie exchanged at /auth/refresh.
// Only the non-secret user profile is persisted for instant UI.
import { create } from "zustand";
import { ApiError, api, setAuthToken, setRefreshHandler } from "./api";

export interface Identity {
  userId: string;
  orgId: string;
  role: string;
  email?: string;
}

/** The body of a successful sign-in, refresh or desktop sign-in response. */
export interface AuthResponse {
  accessToken: string;
  user: {
    user_id: string;
    org_id: string;
    role: string;
    email: string;
  };
}

const USER_KEY = "rowset.user";

interface AuthState {
  token: string | null;
  user: Identity | null;
  // "restoring" while the refresh-cookie exchange for a persisted session is
  // in flight; route guards should wait instead of bouncing to /login.
  status: "restoring" | "ready";
  logout: () => void;
}

function toIdentity(res: AuthResponse): Identity {
  return {
    userId: res.user.user_id,
    orgId: res.user.org_id,
    role: res.user.role,
    email: res.user.email,
  };
}

function applySession(res: AuthResponse): Identity {
  const user = toIdentity(res);
  localStorage.setItem(USER_KEY, JSON.stringify(user));
  setAuthToken(res.accessToken);
  return user;
}

function clearSession() {
  localStorage.removeItem(USER_KEY);
  setAuthToken(null);
}

const persistedUser = safeParse(localStorage.getItem(USER_KEY));

export const useAuth = create<AuthState>(() => ({
  token: null,
  user: persistedUser,
  // The HttpOnly cookie is the source of truth. Always attempt restoration:
  // localStorage may have been cleared independently while the server session
  // is still valid, and treating that as signed out made reloads unreliable.
  status: "restoring",

  logout: () => {
    // Revokes the refresh-token family and clears the cookie server-side.
    void api("/auth/logout", { method: "POST" }).catch(() => undefined);
    clearSession();
    useAuth.setState({ token: null, user: null, status: "ready" });
  },
}));

// Exchanges the HttpOnly refresh cookie for a fresh access token. Used on
// boot and by the api layer when a request 401s.
async function tryRefresh(): Promise<string | null> {
  for (let attempt = 0; attempt < 3; attempt += 1) {
    try {
      const res = await api<AuthResponse | undefined>("/auth/refresh", { method: "POST" });
      if (!res) break;
      const user = applySession(res);
      useAuth.setState({ token: res.accessToken, user, status: "ready" });
      return res.accessToken;
    } catch (error) {
      // A missing/expired cookie is definitive. Network failures and 5xx
      // responses are not: a VM or reverse proxy can be briefly unavailable
      // during startup, and must not turn a healthy session into a logout.
      if (error instanceof ApiError && error.status === 401) break;
      if (attempt < 2) await new Promise((resolve) => window.setTimeout(resolve, 250 * (attempt + 1)));
    }
  }
  clearSession();
  useAuth.setState({ token: null, user: null, status: "ready" });
  return null;
}

/** Starts a session from a sign-in response, such as a sign-in form's. */
export function startSession(res: AuthResponse) {
  const user = applySession(res);
  useAuth.setState({ token: res.accessToken, user, status: "ready" });
}

// initAuth installs the refresh hook and restores a persisted session.
export function initAuth() {
  setRefreshHandler(tryRefresh);
  const ticket = new URLSearchParams(window.location.hash.slice(1)).get("local");
  if (ticket) {
    window.history.replaceState(null, "", window.location.pathname + window.location.search);
    void api<AuthResponse>("/auth/local", { method: "POST", body: JSON.stringify({ ticket }) })
      .then(startSession)
      .catch(() => void tryRefresh());
  } else void tryRefresh();
}

function safeParse(raw: string | null): Identity | null {
  if (!raw) return null;
  try {
    return JSON.parse(raw) as Identity;
  } catch {
    return null;
  }
}
