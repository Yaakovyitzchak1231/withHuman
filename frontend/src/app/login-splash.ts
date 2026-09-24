/*
 * The sign-in splash plays once per login. Sign-in inside the session
 * gate flips state directly; the signup and invitation ceremonies end
 * in a full navigation instead, so they leave this one-shot flag for
 * the gate to consume on its next load.
 */
const KEY = "wh-login-splash";

export function requestLoginSplash() {
  try {
    sessionStorage.setItem(KEY, "1");
  } catch {
    /* storage unavailable: the splash is decoration */
  }
}

export function consumeLoginSplash(): boolean {
  try {
    const requested = sessionStorage.getItem(KEY) === "1";
    sessionStorage.removeItem(KEY);
    return requested;
  } catch {
    return false;
  }
}
