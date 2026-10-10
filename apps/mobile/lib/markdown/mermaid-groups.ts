/**
 * Mermaid fallback for the phone (DENE-1682). The Markdown renderer carries no
 * WebView (see docs/markdown-rendering-adr.md), so a mermaid fence cannot be
 * drawn. 听汇报 replies use one shape — a flowchart of `subgraph` groups with
 * labelled nodes and no edges — which reads fine as grouped lists with counts.
 * Anything that is not that shape returns null and stays a plain code block.
 */
export interface MermaidGroup {
  title: string;
  items: string[];
}

const HEADER = /^(flowchart|graph)\b/i;
const SUBGRAPH = /^subgraph\s+(.+)$/i;
// `ID["label"]`, `ID[label]`, `ID("label")`, `ID{{"label"}}` … the first
// quoted or bracketed text after the id.
const NODE = /^[\w-]+\s*[[({]+\s*"?([^"\])}]+?)"?\s*[\])}]+\s*$/;

function unquote(text: string): string {
  // `subgraph one [Title]` / `subgraph "Title"` / `subgraph Title`.
  const bracket = /\[\s*"?([^"\]]+?)"?\s*\]\s*$/.exec(text);
  if (bracket) return bracket[1].trim();
  return text.replace(/^"(.*)"$/, "$1").trim();
}

export function parseMermaidGroups(code: string): MermaidGroup[] | null {
  const lines = code
    .split("\n")
    .map((l) => l.trim())
    .filter((l) => l && !l.startsWith("%%"));
  if (lines.length === 0 || !HEADER.test(lines[0])) return null;

  const groups: MermaidGroup[] = [];
  let current: MermaidGroup | null = null;
  for (const line of lines.slice(1)) {
    const sub = SUBGRAPH.exec(line);
    if (sub) {
      if (current) return null; // nested groups: not the report shape
      current = { title: unquote(sub[1]), items: [] };
      continue;
    }
    if (/^end$/i.test(line)) {
      if (!current) return null;
      groups.push(current);
      current = null;
      continue;
    }
    // Edges, styles, directions inside a group mean a real diagram.
    if (!current) {
      if (/^(direction|classDef|class|style|linkStyle)\b/i.test(line)) continue;
      return null;
    }
    if (/^direction\b/i.test(line)) continue;
    const node = NODE.exec(line);
    if (!node) return null;
    current.items.push(node[1].trim());
  }
  if (current || groups.length === 0) return null;
  return groups;
}
