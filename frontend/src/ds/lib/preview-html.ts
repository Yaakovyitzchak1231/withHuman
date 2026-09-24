/* Build a passive fragment in an inert template. Only structural HTML
 * survives: no author CSS, event handlers, URLs, forms or refresh tags.
 * The frame's sandbox and CSP are independent additional boundaries.
 * The source toggle always shows the untouched input. */
const tags = new Set([
  "a", "abbr", "b", "blockquote", "br", "caption", "code", "dd", "del",
  "div", "dl", "dt", "em", "figcaption", "figure", "h1", "h2", "h3",
  "h4", "h5", "h6", "hr", "i", "ins", "li", "ol", "p", "pre", "s",
  "small", "span", "strong", "sub", "sup", "table", "tbody", "td",
  "tfoot", "th", "thead", "tr", "u", "ul",
]);
const omit = new Set(["script", "style", "template", "noscript"]);

export function previewHTML(html: string): string {
  const template = document.createElement("template");
  template.innerHTML = html;
  const clean = document.createElement("template");
  function copy(node: Node, parent: Node) {
    if (node.nodeType === Node.TEXT_NODE) {
      parent.appendChild(document.createTextNode(node.textContent ?? ""));
      return;
    }
    if (node.nodeType !== Node.ELEMENT_NODE) return;
    const element = node as Element;
    const tag = element.localName;
    if (omit.has(tag)) return;
    if (tag === "img") {
      parent.appendChild(document.createTextNode("[image" + (element.getAttribute("alt") ? ": " + element.getAttribute("alt") : "") + "]"));
      return;
    }
    const output = tags.has(tag) ? document.createElement(tag) : parent;
    if (output !== parent) parent.appendChild(output);
    for (const child of node.childNodes) copy(child, output);
  }
  for (const child of template.content.childNodes) copy(child, clean.content);
  return clean.innerHTML;
}
