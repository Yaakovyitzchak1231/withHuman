/*
 * The sign-in method a person used last, so the access screen can mark
 * it. A method is staged the moment an attempt starts (the password
 * submit, the hop to a social provider or an identity provider) and
 * committed once a session exists, whichever page that session first
 * appears on. Failed attempts leave only a stage, which the next attempt
 * replaces; the last committed method survives in localStorage, next to
 * the remembered email.
 */
export type LoginMethod =
  | { kind: "password" }
  | { kind: "social"; provider: string }
  | { kind: "sso"; organization: string };

const LAST_KEY = "wh-last-login-method";
const STAGED_KEY = "wh-staged-login-method";

function encode(method: LoginMethod): string {
  switch (method.kind) {
    case "password":
      return "password";
    case "social":
      return `social:${method.provider}`;
    case "sso":
      return `sso:${method.organization}`;
  }
}

function decode(raw: string | null): LoginMethod | null {
  if (raw === "password") return { kind: "password" };
  if (raw?.startsWith("social:") && raw.length > 7) {
    return { kind: "social", provider: raw.slice(7) };
  }
  if (raw?.startsWith("sso:") && raw.length > 4) {
    return { kind: "sso", organization: raw.slice(4) };
  }
  return null;
}

export function lastLoginMethod(): LoginMethod | null {
  try {
    return decode(localStorage.getItem(LAST_KEY));
  } catch {
    return null;
  }
}

/* An attempt is under way. Full-navigation flows (social, SSO) leave the
 * page, so the stage lives in sessionStorage, which follows the tab
 * through the provider and back. */
export function stageLoginMethod(method: LoginMethod) {
  try {
    sessionStorage.setItem(STAGED_KEY, encode(method));
  } catch {
    /* storage unavailable: the badge is a convenience */
  }
}

/* A session exists: whatever attempt was staged is the method that
 * produced it. Nothing staged means a session that did not come from a
 * sign-in on this tab (a reload, a new tab), which changes nothing. */
export function commitStagedLoginMethod() {
  try {
    const staged = sessionStorage.getItem(STAGED_KEY);
    if (staged === null) return;
    sessionStorage.removeItem(STAGED_KEY);
    if (decode(staged)) localStorage.setItem(LAST_KEY, staged);
  } catch {
    /* storage unavailable */
  }
}
