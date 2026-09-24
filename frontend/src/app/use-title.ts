import { useEffect } from "react";

/* Browser tab title, "wH: Queue" style. */
export function useTitle(title: string) {
  useEffect(() => {
    document.title = `wH: ${title}`;
  }, [title]);
}
