// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { commitStagedLoginMethod, lastLoginMethod, stageLoginMethod } from "./login-method";
import { stubWebStorage } from "./test-storage";

beforeEach(stubWebStorage);
afterEach(() => {
  vi.unstubAllGlobals();
});

describe("login method memory", () => {
  it("remembers nothing until an attempt is staged and committed", () => {
    expect(lastLoginMethod()).toBeNull();
    stageLoginMethod({ kind: "social", provider: "google" });
    expect(lastLoginMethod()).toBeNull();
    commitStagedLoginMethod();
    expect(lastLoginMethod()).toEqual({ kind: "social", provider: "google" });
    expect(sessionStorage.getItem("wh-staged-login-method")).toBeNull();
  });

  it("keeps the last committed method when a session arrives with nothing staged", () => {
    stageLoginMethod({ kind: "sso", organization: "acme" });
    commitStagedLoginMethod();
    commitStagedLoginMethod();
    expect(lastLoginMethod()).toEqual({ kind: "sso", organization: "acme" });
  });

  it("lets a later attempt replace an abandoned one", () => {
    stageLoginMethod({ kind: "social", provider: "github" });
    stageLoginMethod({ kind: "password" });
    commitStagedLoginMethod();
    expect(lastLoginMethod()).toEqual({ kind: "password" });
  });

  it("ignores values it cannot read", () => {
    localStorage.setItem("wh-last-login-method", "carrier-pigeon");
    expect(lastLoginMethod()).toBeNull();
    localStorage.setItem("wh-last-login-method", "social:");
    expect(lastLoginMethod()).toBeNull();
  });
});
