import { describe, expect, it } from "vitest";
import { parseMermaidGroups } from "./mermaid-groups";

describe("parseMermaidGroups", () => {
  it("reads the 听汇报 shape into groups", () => {
    const code = `flowchart TB
  subgraph 做完
    A["DENE-1643 参考项目"]
  end
  subgraph 在做
    B["DENE-1665 聊天开单卡"]
    C["DENE-1666 另一个"]
  end
  subgraph 等你
    D["DENE-1667 听汇报"]
  end`;
    expect(parseMermaidGroups(code)).toEqual([
      { title: "做完", items: ["DENE-1643 参考项目"] },
      { title: "在做", items: ["DENE-1665 聊天开单卡", "DENE-1666 另一个"] },
      { title: "等你", items: ["DENE-1667 听汇报"] },
    ]);
  });

  it("accepts bracketed titles and unquoted labels", () => {
    const code = "graph LR\nsubgraph g1 [等你]\nA[DENE-1 x]\nend";
    expect(parseMermaidGroups(code)).toEqual([{ title: "等你", items: ["DENE-1 x"] }]);
  });

  it("leaves real diagrams alone", () => {
    expect(parseMermaidGroups("flowchart TB\n A --> B")).toBeNull();
    expect(parseMermaidGroups("sequenceDiagram\n A->>B: hi")).toBeNull();
    expect(parseMermaidGroups("flowchart TB\nsubgraph x\nA --> B\nend")).toBeNull();
    expect(parseMermaidGroups("flowchart TB\nsubgraph x\nA[a]")).toBeNull();
  });
});
