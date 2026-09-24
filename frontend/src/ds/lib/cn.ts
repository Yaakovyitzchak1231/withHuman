import { clsx, type ClassValue } from "clsx";
import { extendTailwindMerge } from "tailwind-merge";

/*
 * tailwind-merge must be taught the custom theme, or it misclassifies:
 * text-body would be read as a text color (clobbering text-ink), and
 * border-hairline as a border color (clobbering border-edge).
 */
const twMerge = extendTailwindMerge({
  extend: {
    classGroups: {
      "font-size": [
        {
          text: [
            "micro",
            "label",
            "body",
            "body-lg",
            "title",
            "title-lg",
            "display",
          ],
        },
      ],
      "border-w": [{ border: ["hairline"] }],
      "border-w-t": [{ "border-t": ["hairline"] }],
      "border-w-b": [{ "border-b": ["hairline"] }],
      "border-w-l": [{ "border-l": ["hairline"] }],
      "border-w-r": [{ "border-r": ["hairline"] }],
      "border-w-x": [{ "border-x": ["hairline"] }],
      "border-w-y": [{ "border-y": ["hairline"] }],
    },
  },
});

export function cn(...inputs: ClassValue[]): string {
  return twMerge(clsx(inputs));
}
