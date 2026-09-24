import { createContext, useContext } from "react";
import type { Principal, Session } from "../types";

/* The authenticated session with its sign-out action, provided by the
 * /_app layout. Session itself is the enriched /me payload. */
export type AppSession = Session & { signOut: () => void };

export const SessionContext = createContext<AppSession | null>(null);

export function useSession(): AppSession {
  const session = useContext(SessionContext);
  if (!session) {
    throw new Error("useSession must be used inside an authenticated route");
  }
  return session;
}

export function usePrincipal(): Principal {
  return useSession().principal;
}
