import { describe, expect, it } from "vitest";
import { formatGoDuration, parseGoDuration } from "./duration";

describe("parseGoDuration", () => {
  it("reads the forms Go prints", () => {
    expect(parseGoDuration("24h0m0s")).toBe(86_400);
    expect(parseGoDuration("15m0s")).toBe(900);
    expect(parseGoDuration("1h30m")).toBe(5_400);
    expect(parseGoDuration("90s")).toBe(90);
    expect(parseGoDuration("1.5h")).toBe(5_400);
    expect(parseGoDuration("0")).toBe(0);
  });

  it("refuses anything that is not a duration", () => {
    expect(parseGoDuration("")).toBeNull();
    expect(parseGoDuration("24")).toBeNull();
    expect(parseGoDuration("24 hours")).toBeNull();
    expect(parseGoDuration("1h junk")).toBeNull();
  });
});

describe("formatGoDuration", () => {
  it("picks the largest whole unit", () => {
    expect(formatGoDuration(86_400)).toBe("24h");
    expect(formatGoDuration(900)).toBe("15m");
    expect(formatGoDuration(5_400)).toBe("90m");
    expect(formatGoDuration(90)).toBe("90s");
    expect(formatGoDuration(0)).toBe("0s");
  });

  it("survives a round trip through the parser", () => {
    for (const seconds of [60, 900, 3_600, 28_800, 86_400, 604_800]) {
      expect(parseGoDuration(formatGoDuration(seconds))).toBe(seconds);
    }
  });
});
