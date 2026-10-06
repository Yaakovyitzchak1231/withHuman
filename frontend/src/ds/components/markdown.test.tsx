// @vitest-environment jsdom
import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it } from "vitest";
import { Markdown } from "./markdown";

afterEach(cleanup);

describe("Markdown", () => {
  it("links a bare web address, opening it in a new tab", () => {
    render(<Markdown>{"Fixing the bug in https://github.com/example/repo/issues/42 before the release."}</Markdown>);
    const link = screen.getByRole("link", { name: "https://github.com/example/repo/issues/42" });
    expect(link.getAttribute("href")).toBe("https://github.com/example/repo/issues/42");
    expect(link.getAttribute("target")).toBe("_blank");
    expect(link.getAttribute("rel")).toBe("noopener noreferrer");
  });

  it("renders neither raw HTML nor a script link", () => {
    const { container } = render(<Markdown>{'<b>bold</b> [click](javascript:alert(1))'}</Markdown>);
    expect(container.querySelector("b")).toBeNull();
    expect(screen.getByText("click").getAttribute("href") ?? "").not.toContain("javascript");
  });
});
