// @vitest-environment node
import { describe, expect, it } from "vitest";
import type { ChatMessage, IssueDraftPayload } from "../types";
import {
  decodeIssueDraftInput,
  encodeIssueDraftInput,
  issueDraftIsCreatable,
  issueDraftPendingQuestion,
  mergeIssueDraftPayload,
  parseIssueDraftBlock,
  parseIssueDraftQuestion,
  stripIssueDraftDirectives,
} from "./protocol";

const EMPTY: IssueDraftPayload = {
  title: "",
  description: "",
  status: "",
  priority: "",
};

describe("parseIssueDraftBlock", () => {
  it("reads title and description out of the carrier's block", () => {
    const reply =
      'Here is what I have so far.\n<issue_draft>{"title":"Ship the thing","description":"Do it carefully."}</issue_draft>';
    expect(parseIssueDraftBlock(reply)).toEqual({
      title: "Ship the thing",
      description: "Do it carefully.",
    });
  });

  it("takes the last block when a reply drafts twice", () => {
    const reply =
      '<issue_draft>{"title":"first"}</issue_draft> Actually, narrower:\n<issue_draft>{"title":"second"}</issue_draft>';
    expect(parseIssueDraftBlock(reply)).toEqual({ title: "second" });
  });

  it("drops empty strings rather than treating them as a clear", () => {
    // The carrier is told to leave status/priority empty unless the user
    // states them, so an empty value is "no opinion" — clearing the user's
    // own selection here would silently undo their edit on every reply.
    expect(parseIssueDraftBlock('<issue_draft>{"title":"T","status":"","priority":""}</issue_draft>')).toEqual(
      { title: "T" },
    );
  });

  it("returns null for a missing, unterminated or unparseable block", () => {
    expect(parseIssueDraftBlock("no block here")).toBeNull();
    expect(parseIssueDraftBlock('<issue_draft>{"title":"streaming')).toBeNull();
    expect(parseIssueDraftBlock("<issue_draft>not json</issue_draft>")).toBeNull();
    expect(parseIssueDraftBlock("<issue_draft>[1,2]</issue_draft>")).toBeNull();
  });

  it("recovers JSON whose string values contain literal newlines", () => {
    const reply = '<issue_draft>{"title":"T","description":"line one\nline two"}</issue_draft>';
    expect(parseIssueDraftBlock(reply)).toEqual({
      title: "T",
      description: "line one\nline two",
    });
  });

  it("reads a block the model wrote after prose, with Markdown in the fields", () => {
    // The carrier's real replies put the block last, after blank-line-separated
    // prose, with escaped newlines and quotes inside the JSON strings.
    const reply = [
      "先给一版可执行的草稿，再问你一个最影响实现的问题。",
      "",
      "**最关键的一点：批量的“范围”是什么？**",
      "",
      '<issue_draft>{"title":"收件箱支持批量标记已读","description":"## 问题\\n\\n收件箱目前只能逐条标记已读。\\n\\n## 验收标准\\n- 支持「全选」\\n- 操作幂等","status":"","priority":""}</issue_draft>',
    ].join("\n");
    expect(parseIssueDraftBlock(reply)).toEqual({
      title: "收件箱支持批量标记已读",
      description:
        "## 问题\n\n收件箱目前只能逐条标记已读。\n\n## 验收标准\n- 支持「全选」\n- 操作幂等",
    });
  });

  it("reads a block that follows the guided question block", () => {
    const reply =
      'Who should run it?\n<issue_draft_question>{"question":"Who should run it?"}</issue_draft_question>\n<issue_draft>{"title":"Dark mode"}</issue_draft>';
    expect(parseIssueDraftBlock(reply)).toEqual({ title: "Dark mode" });
    expect(parseIssueDraftQuestion(reply)?.question).toBe("Who should run it?");
  });

  it("reads a block the model fenced, and ignores the fields it does not own", () => {
    expect(
      parseIssueDraftBlock(
        'Draft:\n\n```json\n<issue_draft>{"title":"T","assignee_id":"x","notes":7}</issue_draft>\n```',
      ),
    ).toEqual({ title: "T" });
  });

  it("returns null for every malformed JSON body the block might carry", () => {
    // Each of these is "no preview update, keep the conversation": a trailing
    // comma, a single-quoted key, a JSON array, a bare null and a truncated
    // object are all shapes a CLI-backed model has produced at least once.
    const bodies = [
      '{"title":"T",}',
      "{'title':'T'}",
      "[1,2]",
      "null",
      '{"title":"T"',
      '{"title","T"}',
      "",
    ];
    for (const body of bodies) {
      expect(parseIssueDraftBlock(`<issue_draft>${body}</issue_draft>`)).toBeNull();
    }
  });

  it("reads the group, its stages and its hints", () => {
    // What the prompt asks for: a key per sub-issue, a 1-based stage, and a
    // hint at the kind of work rather than an assignee id.
    const reply = `<issue_draft>${JSON.stringify({
      title: "T",
      children: [
        {
          key: "c1",
          title: "Backend",
          description: "Do it",
          stage: 1,
          assignee_hint: "backend",
        },
        { key: "c2", title: "Frontend", stage: 2, assignee_hint: "frontend" },
      ],
    })}</issue_draft>`;
    expect(parseIssueDraftBlock(reply)?.children).toEqual([
      {
        key: "c1",
        title: "Backend",
        description: "Do it",
        status: "todo",
        priority: "none",
        assignee_type: null,
        assignee_id: null,
        stage: 1,
        assignee_hint: "backend",
      },
      {
        key: "c2",
        title: "Frontend",
        description: "",
        status: "backlog",
        priority: "none",
        assignee_type: null,
        assignee_id: null,
        stage: 2,
        assignee_hint: "frontend",
      },
    ]);
  });

  it("derives each sub-issue's status from its stage", () => {
    // This is the whole dispatch rule: stage 1 runs the moment the group is
    // created, later stages park in Backlog with their assignee already bound.
    const parsed = parseIssueDraftBlock(
      `<issue_draft>{"title":"T","children":[{"key":"c1","title":"First","stage":1},{"key":"c2","title":"Later","stage":2}]}</issue_draft>`,
    );
    expect(parsed?.children?.map((child) => child.status)).toEqual([
      "todo",
      "backlog",
    ]);
  });

  it("mints a key for a sub-issue the carrier left unnamed", () => {
    // The server refuses a keyless sub-issue, and two nodes sharing a key
    // derive one identity — a 400 or a 500 instead of a group.
    const parsed = parseIssueDraftBlock(
      `<issue_draft>{"title":"T","children":[{"title":"No key"},{"key":"c1","title":"Taken"},{"key":"c1","title":"Duplicate"}]}</issue_draft>`,
    );
    expect(parsed?.children?.map((child) => child.key)).toEqual([
      "c1",
      "c2",
      "c3",
    ]);
  });

  it("drops a sub-issue with no title", () => {
    const parsed = parseIssueDraftBlock(
      `<issue_draft>{"title":"T","children":[{"key":"c1","title":"  "},{"key":"c2","title":"Kept"}]}</issue_draft>`,
    );
    expect(parsed?.children?.map((child) => child.key)).toEqual(["c2"]);
  });

  it("ignores an assignee the carrier named", () => {
    // It has no roster and its instructions forbid this, so the panel is the
    // only thing that may choose one.
    const parsed = parseIssueDraftBlock(
      `<issue_draft>{"title":"T","children":[{"key":"c1","title":"Child","assignee_type":"agent","assignee_id":"made-up"}]}</issue_draft>`,
    );
    expect(parsed?.children?.[0]?.assignee_id).toBeNull();
    expect(parsed?.children?.[0]?.assignee_type).toBeNull();
  });

  it("says nothing about sub-issues when the block does not mention them", () => {
    // A reply that only restated the title must not delete the user's group.
    expect(
      parseIssueDraftBlock('<issue_draft>{"title":"T"}</issue_draft>'),
    ).toEqual({ title: "T" });
  });

  it("treats a malformed array as no update rather than a clear", () => {
    expect(
      parseIssueDraftBlock(
        '<issue_draft>{"title":"T","children":"two"}</issue_draft>',
      ),
    ).toEqual({ title: "T" });
    expect(
      parseIssueDraftBlock(
        '<issue_draft>{"title":"T","children":[{"description":"no title"}]}</issue_draft>',
      ),
    ).toEqual({ title: "T" });
  });

  it("lets an explicit empty array clear the group", () => {
    // The one way the conversation can say "this went back to a single issue".
    expect(
      parseIssueDraftBlock('<issue_draft>{"title":"T","children":[]}</issue_draft>')
        ?.children,
    ).toEqual([]);
  });

  it("caps the group at the server's limit", () => {
    const many = Array.from({ length: 25 }, (_, index) => ({
      key: `c${index + 1}`,
      title: `Child ${index + 1}`,
    }));
    expect(
      parseIssueDraftBlock(
        `<issue_draft>${JSON.stringify({ title: "T", children: many })}</issue_draft>`,
      )?.children,
    ).toHaveLength(20);
  });

  it("keeps the fields it can read when the block is only partly filled in", () => {
    // A missing field is "no opinion", not a wipe: the carrier is told to leave
    // status and priority empty unless the user stated them.
    expect(
      parseIssueDraftBlock('<issue_draft>{"title":"Only a title"}</issue_draft>'),
    ).toEqual({ title: "Only a title" });
    expect(
      parseIssueDraftBlock('<issue_draft>{"description":"Only a body"}</issue_draft>'),
    ).toEqual({ description: "Only a body" });
    expect(
      parseIssueDraftBlock(
        '<issue_draft>{"title":"T","status":7,"priority":null}</issue_draft>',
      ),
    ).toEqual({ title: "T" });
  });
});

describe("stripIssueDraftDirectives", () => {
  it("removes complete and still-streaming blocks", () => {
    expect(stripIssueDraftDirectives('Done.\n<issue_draft>{"title":"T"}</issue_draft>')).toBe(
      "Done.",
    );
    expect(stripIssueDraftDirectives('Working…\n<issue_draft>{"title":"T"')).toBe("Working…");
  });

  it("removes the question block too", () => {
    // Both blocks are machine-readable. Leaving the question block in would
    // print raw JSON above the answer chips.
    expect(
      stripIssueDraftDirectives(
        'Who runs it?\n<issue_draft_question>{"question":"Who runs it?"}</issue_draft_question>',
      ),
    ).toBe("Who runs it?");
    expect(
      stripIssueDraftDirectives(
        'Who runs it?\n<issue_draft_question>{"question":"Who runs',
      ),
    ).toBe("Who runs it?");
  });

  it("leaves a reply with no block untouched", () => {
    expect(stripIssueDraftDirectives("Just a question?")).toBe("Just a question?");
  });

  /**
   * The shapes below are the ones the CLI-backed carriers actually produced in
   * the DENE-317 acceptance run: prose paragraphs, a blank line, then one block
   * whose JSON holds real Markdown with escaped newlines, sometimes with a
   * trailing blank line, sometimes with the guided question block above it.
   * They are here because a strip that only handles the tidy one-line fixture
   * is exactly how the raw block reached the transcript.
   */
  it("strips the block out of a reply written as prose, blank line, block", () => {
    const reply = [
      "两处已落进草稿：分段头只显示任务数，开关默认关闭已写死为验收项。",
      "",
      "标题保留你人工校订后的版本；描述字段这次传过来是空的，我把上一版内容带回并合并了这两点。",
      "",
      '<issue_draft>{"title":"任务看板支持按周分组视图（人工校订）","description":"## 问题\\n\\n当前任务看板只能按状态（列）查看。\\n\\n## 验收标准\\n- 周起始为周一","status":"","priority":""}</issue_draft>',
    ].join("\n");
    expect(stripIssueDraftDirectives(reply)).toBe(
      [
        "两处已落进草稿：分段头只显示任务数，开关默认关闭已写死为验收项。",
        "",
        "标题保留你人工校订后的版本；描述字段这次传过来是空的，我把上一版内容带回并合并了这两点。",
      ].join("\n"),
    );
  });

  it("strips a block that has prose after it, and one with a trailing blank line", () => {
    expect(
      stripIssueDraftDirectives(
        'Drafting now.\n<issue_draft>{"title":"T"}</issue_draft>\nAnything else?',
      ),
    ).toBe("Drafting now.\nAnything else?");
    expect(
      stripIssueDraftDirectives('已为您更新需求草稿：\n\n<issue_draft>{"title":"T"}</issue_draft>\n'),
    ).toBe("已为您更新需求草稿：");
  });

  it("strips the question block the guided policy puts above the draft block", () => {
    const reply =
      'Who should run it?\n<issue_draft_question>{"question":"Who should run it?","options":[{"label":"A bot","value":"Assign a bot","recommended":true}]}</issue_draft_question>\n<issue_draft>{"title":"Dark mode"}</issue_draft>';
    expect(stripIssueDraftDirectives(reply)).toBe("Who should run it?");
  });

  it("strips an unterminated block that follows prose, and an unterminated question block", () => {
    // A reply still streaming, or one the model never closed: whatever follows
    // the opening tag is machine-readable and must not reach the screen.
    expect(
      stripIssueDraftDirectives(
        'Prose first.\n<issue_draft>{"title":"T","description":"still arr',
      ),
    ).toBe("Prose first.");
    expect(
      stripIssueDraftDirectives('Which surface?\n<issue_draft_question>{"question":"Which'),
    ).toBe("Which surface?");
  });

  it("strips blocks written with CRLF line endings", () => {
    expect(
      stripIssueDraftDirectives('Done.\r\n<issue_draft>{"title":"T"}</issue_draft>\r\n'),
    ).toBe("Done.");
  });

  it("reduces a reply that was nothing but a block to nothing", () => {
    expect(stripIssueDraftDirectives('<issue_draft>{"title":"T"}</issue_draft>')).toBe("");
    expect(stripIssueDraftDirectives('\n<issue_draft>{"title":"T"}</issue_draft>\n')).toBe("");
  });

  it("keeps the block's own markup out while leaving the prose's markup alone", () => {
    // The JSON quotes `##` headings and code spans; only the block is removed,
    // so the surrounding Markdown still renders as the carrier wrote it.
    const reply =
      '一版草稿：\n\n- **范围**：仅勾选可见条目\n\n<issue_draft>{"description":"## 验收标准\\n- 使用 `due date` 字段"}</issue_draft>';
    expect(stripIssueDraftDirectives(reply)).toBe(
      "一版草稿：\n\n- **范围**：仅勾选可见条目",
    );
  });

  /**
   * The remaining shapes are the ones the instructions do not prevent and the
   * tidy fixtures did not cover: a model that fences a machine-readable block
   * the way it fences every other JSON payload, a stream that is cut off in the
   * middle of the tag itself, and prose on both sides of the block. Each one
   * ends the same way — the prose survives, no directive markup reaches the
   * screen, and the conversation does not break.
   */
  it("strips a block the model wrapped in a Markdown fence", () => {
    // The instructions say "do not wrap it in Markdown fences"; a CLI-backed
    // model does it anyway. Removing only the block would leave an empty code
    // box where the machine-readable half used to be.
    const fenced = [
      "一版草稿：",
      "",
      "```json",
      '<issue_draft>{"title":"Fenced"}</issue_draft>',
      "```",
    ].join("\n");
    expect(stripIssueDraftDirectives(fenced)).toBe("一版草稿：");

    expect(
      stripIssueDraftDirectives(
        'Draft:\n\n~~~xml\n<issue_draft>{"title":"T"}</issue_draft>\n~~~\n\nAnything else?',
      ),
    ).toBe("Draft:\n\nAnything else?");
  });

  it("strips a fenced block that is still streaming", () => {
    // The fence opener precedes the block, so the still-open block removal has
    // to take the opener with it; otherwise the transcript ends in a dangling
    // ``` that renders as an empty code box.
    expect(
      stripIssueDraftDirectives('Drafting…\n\n```json\n<issue_draft>{"title":"T'),
    ).toBe("Drafting…");
    expect(
      stripIssueDraftDirectives(
        'Who runs it?\n```\n<issue_draft_question>{"question":"Who runs',
      ),
    ).toBe("Who runs it?");
  });

  it("strips a fenced question block written above a fenced draft block", () => {
    const reply = [
      "对齐一下：",
      "",
      "```json",
      '<issue_draft_question>{"question":"Who runs it?"}</issue_draft_question>',
      '<issue_draft>{"title":"Dark mode"}</issue_draft>',
      "```",
    ].join("\n");
    expect(stripIssueDraftDirectives(reply)).toBe("对齐一下：");
  });

  it("keeps a code block the prose closed, even when it sits right before the block", () => {
    // The carrier is told to put the block last, so a reply that ends on a
    // snippet puts a closed fence directly above it. Dropping that fence as if
    // it were the leftover of a fenced block would leave the transcript inside
    // an open code block.
    const reply = [
      "这样改：",
      "",
      "```ts",
      "const a = 1;",
      "```",
      '<issue_draft>{"title":"T"}</issue_draft>',
    ].join("\n");
    expect(stripIssueDraftDirectives(reply)).toBe(
      "这样改：\n\n```ts\nconst a = 1;\n```",
    );
  });

  it("strips a partial opening tag at the end of a streaming reply", () => {
    // The block patterns need a complete `<tag>`, so a stream cut mid-tag has
    // nothing to match and would print the fragment.
    expect(stripIssueDraftDirectives("Thinking…\n<issue_draft")).toBe("Thinking…");
    expect(stripIssueDraftDirectives("Thinking…\n<issue_draft_quest")).toBe("Thinking…");
    expect(stripIssueDraftDirectives("Thinking…\n<issue_draft_question")).toBe(
      "Thinking…",
    );
    // A stray "<" that is not the start of a directive is the user's prose.
    expect(stripIssueDraftDirectives("a < b")).toBe("a < b");
  });

  it("strips the block out of prose that continues after it", () => {
    expect(
      stripIssueDraftDirectives(
        'p1。\n\n<issue_draft>{"title":"T"}</issue_draft>\n\np2。',
      ),
    ).toBe("p1。\n\np2。");
  });

  it("strips every block of a reply that mixes both kinds, several times", () => {
    const reply = [
      "先问一个，再给两版草稿：",
      "",
      '<issue_draft_question>{"question":"Which surface?"}</issue_draft_question>',
      "",
      '<issue_draft>{"title":"first"}</issue_draft>',
      "换个说法：",
      '<issue_draft_question>{"question":"Which surface, really?"}</issue_draft_question>',
      '<issue_draft>{"title":"second"}</issue_draft>',
    ].join("\n");
    // The prose that framed the block group rejoins; each block takes the
    // whitespace that separated it, and the paragraphs that owned real blank
    // lines keep them (see the prose/both-sides case above).
    expect(stripIssueDraftDirectives(reply)).toBe("先问一个，再给两版草稿：\n换个说法：");
    expect(parseIssueDraftBlock(reply)).toEqual({ title: "second" });
    expect(parseIssueDraftQuestion(reply)?.question).toBe("Which surface, really?");
  });

  it("leaves no directive markup behind for any malformed shape", () => {
    // The contract with the whole page: a reply the client cannot parse must
    // degrade to the prose the model wrote. Never an exception, and never a raw
    // tag or JSON body in the bubble.
    const shapes = [
      "",
      "no block here",
      "<issue_draft>",
      "<issue_draft></issue_draft>",
      '<issue_draft>{"title":"unterminated',
      "<issue_draft>not json</issue_draft>",
      '<issue_draft>{"title":1,"description":[2]}</issue_draft>',
      '<issue_draft_question>{"question":"q"',
      '<issue_draft_question>{"question":}</issue_draft_question>',
      '<issue_draft_question></issue_draft_question>',
      '<issue_draft_question>{"question":"q","options":"none"}</issue_draft_question>',
      'prose\n```\n<issue_draft_question>{"question":"x"',
      'prose\n<issue_draft_question>{"question":"a\n<issue_draft>{"title":"b',
      '<issue_draft>{"description":"the <issue_draft_question> block"}</issue_draft>',
      '<issue_draft_question>{"question":"what does <issue_draft> mean?"}</issue_draft_question>',
      '\r\n<issue_draft>{"title":"T"}</issue_draft>\r\n',
      "```json\n<issue_draft>{}\n```",
      "<issue_draft_question>",
      "~~~\n<issue_draft>{\n~~~",
    ];
    for (const shape of shapes) {
      expect(() => stripIssueDraftDirectives(shape)).not.toThrow();
      expect(() => parseIssueDraftBlock(shape)).not.toThrow();
      expect(() => parseIssueDraftQuestion(shape)).not.toThrow();
      expect(stripIssueDraftDirectives(shape)).not.toContain("issue_draft");
    }
  });
});

/**
 * The guided policy asks one question at a time and offers answers. The block is
 * a contract with the server prompt, and it fails the same way the draft block
 * does: a model that emits slightly malformed JSON must cost the chips, never
 * the conversation.
 */
describe("parseIssueDraftQuestion", () => {
  const block = (json: string) =>
    `Who should run it?\n<issue_draft_question>${json}</issue_draft_question>`;

  it("reads the question, its options and the recommended one", () => {
    expect(
      parseIssueDraftQuestion(
        block(
          '{"question":"Who runs it?","options":[{"label":"Bot","value":"Assign a bot","recommended":true},{"label":"Me","value":"Leave it unassigned"}]}',
        ),
      ),
    ).toEqual({
      question: "Who runs it?",
      options: [
        { label: "Bot", value: "Assign a bot", recommended: true },
        { label: "Me", value: "Leave it unassigned", recommended: false },
      ],
    });
  });

  it("keeps a question that has no usable options", () => {
    // The composer is the custom answer, so a question with nothing to click is
    // still a question the user can answer.
    expect(parseIssueDraftQuestion(block('{"question":"What is the deadline?"}'))).toEqual({
      question: "What is the deadline?",
      options: [],
    });
  });

  it("drops unusable options instead of the whole question", () => {
    expect(
      parseIssueDraftQuestion(
        block(
          '{"question":"Which?","options":[{"label":"","value":"x"},{"label":"A","value":"  "},{"label":"B","value":"b"},7]}',
        ),
      ),
    ).toEqual({
      question: "Which?",
      options: [{ label: "B", value: "b", recommended: false }],
    });
  });

  it("takes the last complete block, like the draft block does", () => {
    expect(
      parseIssueDraftQuestion(
        '<issue_draft_question>{"question":"First"}</issue_draft_question>\n<issue_draft_question>{"question":"Second"}</issue_draft_question>',
      )?.question,
    ).toBe("Second");
  });

  it("returns null for anything unusable", () => {
    expect(parseIssueDraftQuestion("no block")).toBeNull();
    expect(parseIssueDraftQuestion('<issue_draft_question>{"question":"streaming')).toBeNull();
    expect(parseIssueDraftQuestion(block("not json"))).toBeNull();
    expect(parseIssueDraftQuestion(block('{"question":"   "}'))).toBeNull();
    expect(parseIssueDraftQuestion(block('{"options":[]}'))).toBeNull();
  });

  it("reads a question the model wrote in prose, in a fence, or with CRLF", () => {
    // The shapes a real reply comes in. The block is parsed wherever it sits:
    // the parser is looking for the tag, not for a tidy one-line reply.
    expect(
      parseIssueDraftQuestion(
        "先说结论：默认关闭。\n\n```json\n" +
          '<issue_draft_question>{"question":"Which surface?"}</issue_draft_question>' +
          "\n```\n\n还有一个问题。",
      )?.question,
    ).toBe("Which surface?");
    expect(
      parseIssueDraftQuestion(
        '开头。\r\n<issue_draft_question>{"question":"Which surface?"}</issue_draft_question>\r\n结尾。',
      )?.question,
    ).toBe("Which surface?");
  });

  it("never throws on a malformed block, whatever the field types are", () => {
    const malformed = [
      '{"question":"q","options":{"label":"A","value":"a"}}',
      '{"question":"q","options":[null,7,"A",[],{"label":"B","value":"b"}]}',
      '{"question":{"nested":"object"}}',
      '{"question":"q","options":[{"label":1,"value":2}]}',
      '{"question":"q","options":[{"label":"A","value":"a","recommended":"yes"}]}',
      '{"question":"line one\nline two"}',
      '[{"question":"q"}]',
      "null",
    ];
    for (const json of malformed) {
      expect(() => parseIssueDraftQuestion(block(json))).not.toThrow();
    }
    // A non-array options list is "a question nobody offered answers to", not a
    // reason to lose the question: the composer is the custom answer.
    expect(parseIssueDraftQuestion(block('{"question":"q","options":"none"}'))).toEqual({
      question: "q",
      options: [],
    });
    // Unusable options are dropped one by one; "recommended" is only true when
    // the model literally said true.
    expect(
      parseIssueDraftQuestion(
        block(
          '{"question":"q","options":[null,7,{"label":"A","value":"a","recommended":"yes"},{"label":"B","value":"b","recommended":true}]}',
        ),
      ),
    ).toEqual({
      question: "q",
      options: [
        { label: "A", value: "a", recommended: false },
        { label: "B", value: "b", recommended: true },
      ],
    });
    // A question with a literal newline inside its string is repaired, like the
    // draft block's description.
    expect(parseIssueDraftQuestion(block('{"question":"line one\nline two"}'))?.question).toBe(
      "line one\nline two",
    );
    // A JSON array is not a question block.
    expect(parseIssueDraftQuestion(block('[{"question":"q"}]'))).toBeNull();
    expect(parseIssueDraftQuestion(block("null"))).toBeNull();
  });

  it("is unaffected by a draft block in the same reply, whichever comes first", () => {
    expect(
      parseIssueDraftQuestion(
        '<issue_draft>{"title":"T"}</issue_draft>\n<issue_draft_question>{"question":"After the draft"}</issue_draft_question>',
      )?.question,
    ).toBe("After the draft");
    expect(
      parseIssueDraftQuestion(
        '<issue_draft_question>{"question":"Before the draft"}</issue_draft_question>\n<issue_draft>{"title":"T"}</issue_draft>',
      )?.question,
    ).toBe("Before the draft");
  });
});

describe("issueDraftPendingQuestion", () => {
  const assistant = (id: string, content: string): ChatMessage =>
    ({ id, chat_session_id: "s", role: "assistant", content, created_at: "" }) as ChatMessage;
  const user = (id: string, content: string): ChatMessage =>
    ({ id, chat_session_id: "s", role: "user", content, created_at: "" }) as ChatMessage;

  it("is the question on the last message", () => {
    const pending = issueDraftPendingQuestion([
      assistant("m1", "Drafting.\n<issue_draft>{}"),
      assistant("m2", 'Which surfaces?\n<issue_draft_question>{"question":"Which surfaces?"}</issue_draft_question>'),
    ]);
    expect(pending?.messageId).toBe("m2");
    expect(pending?.question.question).toBe("Which surfaces?");
  });

  it("clears itself once the user answers", () => {
    // The user's own turn is the last message, so the chips disappear without
    // anything having to remember that they were clicked.
    const pending = issueDraftPendingQuestion([
      assistant("m2", '<issue_draft_question>{"question":"Which surfaces?"}</issue_draft_question>'),
      user("m3", "Settings and the issue list"),
    ]);
    expect(pending).toBeNull();
  });

  it("is null for a carrier reply that asks nothing, and for empty transcripts", () => {
    expect(issueDraftPendingQuestion([assistant("m1", "Done.")])).toBeNull();
    expect(issueDraftPendingQuestion([])).toBeNull();
  });

  it("offers the chips for a fenced question and withdraws them once answered", () => {
    // The affordance has to survive the same real-world shapes as the parser:
    // a fenced block is still a question the user can click.
    const fenced = assistant(
      "m2",
      "Which surface?\n\n```json\n" +
        '<issue_draft_question>{"question":"Which surface?","options":[{"label":"Settings","value":"Settings","recommended":true}]}</issue_draft_question>' +
        "\n```",
    );
    expect(issueDraftPendingQuestion([fenced])?.question.options).toEqual([
      { label: "Settings", value: "Settings", recommended: true },
    ]);
    expect(issueDraftPendingQuestion([fenced, user("m3", "Settings")])).toBeNull();
  });
});

describe("issue draft input envelope", () => {
  it("round-trips the user's own words", () => {
    const encoded = encodeIssueDraftInput("add dark mode", {
      ...EMPTY,
      title: "Dark mode",
    });
    expect(decodeIssueDraftInput(encoded)).toBe("add dark mode");
  });

  it("carries the current draft so the carrier can preserve it", () => {
    const encoded = encodeIssueDraftInput("keep going", {
      ...EMPTY,
      title: "T",
      description: "D",
    });
    expect(JSON.parse(encoded.split("\n")[1]!)).toEqual({
      user_request: "keep going",
      current_draft: { title: "T", description: "D", status: "", priority: "" },
    });
  });

  it("carries the group so the carrier can keep its keys", () => {
    // The instructions say a key already emitted must come back unchanged, and
    // the carrier can only keep that promise for a group it is shown.
    const encoded = encodeIssueDraftInput("split it", {
      ...EMPTY,
      title: "Parent",
      children: [
        {
          key: "c1",
          title: "First",
          description: "D",
          status: "todo",
          priority: "none",
          stage: 1,
          assignee_hint: "backend",
        },
      ],
    });
    const envelope = JSON.parse(encoded.split("\n")[1]!);
    expect(envelope.current_draft.children).toEqual([
      {
        key: "c1",
        title: "First",
        description: "D",
        stage: 1,
        assignee_hint: "backend",
      },
    ]);
  });

  it("omits an empty group rather than stating there is none", () => {
    const encoded = encodeIssueDraftInput("keep going", {
      ...EMPTY,
      title: "T",
      children: [],
    });
    expect(
      JSON.parse(encoded.split("\n")[1]!).current_draft,
    ).not.toHaveProperty("children");
  });

  it("returns anything that is not an envelope unchanged", () => {
    expect(decodeIssueDraftInput("plain text")).toBe("plain text");
    expect(decodeIssueDraftInput("MULTICA_ISSUE_DRAFT_INPUT\nnot json")).toBe(
      "MULTICA_ISSUE_DRAFT_INPUT\nnot json",
    );
  });
});

describe("mergeIssueDraftPayload", () => {
  it("overwrites only the fields the reply has an opinion about", () => {
    const current: IssueDraftPayload = {
      ...EMPTY,
      title: "Old",
      description: "Kept",
      priority: "high",
      project_id: "p1",
    };
    expect(mergeIssueDraftPayload(current, { title: "New" })).toEqual({
      ...current,
      title: "New",
    });
  });

  it("is a no-op when the reply had no parseable block", () => {
    expect(mergeIssueDraftPayload(EMPTY, null)).toBe(EMPTY);
  });

  it("keeps the group the reply did not mention", () => {
    // The block is a partial update. A reply that only restated the title must
    // not read as "there are no sub-issues".
    const current: IssueDraftPayload = {
      ...EMPTY,
      title: "Parent",
      children: [
        {
          key: "c1",
          title: "Child",
          description: "",
          status: "todo",
          priority: "none",
        },
      ],
    };
    expect(mergeIssueDraftPayload(current, { title: "New" }).children).toEqual(
      current.children,
    );
  });

  it("replaces the sub-issue set so a removal can land", () => {
    const current: IssueDraftPayload = {
      ...EMPTY,
      title: "Parent",
      children: [
        { key: "c1", title: "One", description: "", status: "todo", priority: "none" },
        { key: "c2", title: "Two", description: "", status: "todo", priority: "none" },
      ],
    };
    const merged = mergeIssueDraftPayload(current, {
      children: [
        { key: "c2", title: "Two, revised", description: "", status: "", priority: "" },
      ],
    });
    expect(merged.children?.map((child) => child.key)).toEqual(["c2"]);
    expect(merged.children?.[0]?.title).toBe("Two, revised");
  });

  it("keeps the assignee the user picked for a row that survives", () => {
    // The carrier is not allowed to choose an assignee and does not know which
    // one was chosen; if replacement wiped it, every later reply would quietly
    // unassign work the user had already routed.
    const current: IssueDraftPayload = {
      ...EMPTY,
      title: "Parent",
      children: [
        {
          key: "c1",
          title: "One",
          description: "",
          status: "todo",
          priority: "none",
          assignee_type: "agent",
          assignee_id: "ag-1",
          assignee_hint: "backend",
        },
      ],
    };
    const merged = mergeIssueDraftPayload(current, {
      children: [
        {
          key: "c1",
          title: "One, revised",
          description: "",
          status: "todo",
          priority: "none",
          assignee_hint: "backend",
        },
      ],
    });
    expect(merged.children?.[0]?.assignee_id).toBe("ag-1");
    expect(merged.children?.[0]?.assignee_type).toBe("agent");
  });

  it("does not carry a surviving row's description forward", () => {
    // This pins the merge as it stands: replacing the group carries over the
    // client-owned assignee fields of a row that is still there, and nothing
    // else. Description is ours to overwrite, not to restore.
    //
    // It protects the ability to REWRITE a sub-issue's description. The
    // alignment contract's front-end section tells the carrier to repeat a
    // child's screen spec in every later block, and that rule is kept by the
    // prompt, not by this merge. If description were also restored by key, a
    // child that came back with an empty description would silently resurrect
    // the previous text, and "change how this screen is written" would become
    // impossible to express — the same argument as the whole-set replacement
    // above.
    const current: IssueDraftPayload = {
      ...EMPTY,
      title: "Parent",
      children: [
        {
          key: "c1",
          title: "Filter bar",
          description: "## 前端做法\n- Name: issue-filter-bar",
          status: "todo",
          priority: "none",
          assignee_type: "agent",
          assignee_id: "ag-1",
          assignee_hint: "frontend page",
        },
      ],
    };
    const merged = mergeIssueDraftPayload(current, {
      children: [
        {
          key: "c1",
          title: "Filter bar",
          description: "",
          status: "todo",
          priority: "none",
          assignee_hint: "frontend page",
        },
      ],
    });
    expect(merged.children?.[0]?.description).toBe("");
    expect(merged.children?.[0]?.assignee_id).toBe("ag-1");
    expect(merged.children?.[0]?.assignee_type).toBe("agent");
    expect(merged.children?.[0]?.assignee_hint).toBe("frontend page");
  });

  it("keeps the project the reply never mentions", () => {
    // The project is client-owned for the same reason the assignee is: the
    // carrier has no project list and is not asked to pick one. Folding a reply
    // in must therefore leave what the preview panel chose alone.
    const parsed = parseIssueDraftBlock(
      '<issue_draft>{"title":"New","description":"D","status":"todo","priority":"high"}</issue_draft>',
    );
    const merged = mergeIssueDraftPayload(
      { ...EMPTY, title: "Old", project_id: "p1" },
      parsed,
    );
    expect(merged.title).toBe("New");
    expect(merged.project_id).toBe("p1");
  });

  it("drops a project the reply tried to name anyway", () => {
    // The carrier still cannot write a project id. A model that invents
    // `project_id` cannot route the group into a project nobody chose.
    const parsed = parseIssueDraftBlock(
      '<issue_draft>{"title":"T","project_id":"invented"}</issue_draft>',
    );
    expect(parsed).not.toHaveProperty("project_id");
    expect(parsed).not.toHaveProperty("project_proposal");
    expect(
      mergeIssueDraftPayload({ ...EMPTY, project_id: "p1" }, parsed).project_id,
    ).toBe("p1");
  });

  it("keeps a project proposal by name and leaves the chosen id alone", () => {
    const parsed = parseIssueDraftBlock(
      '<issue_draft>{"title":"T","project":{"action":"create","name":"通力电梯","icon":"🛗","description":"图像追溯"}}</issue_draft>',
    );
    expect(parsed?.project_proposal).toEqual({
      action: "create",
      name: "通力电梯",
      icon: "🛗",
      description: "图像追溯",
    });
    const merged = mergeIssueDraftPayload(
      { ...EMPTY, project_id: "p1", project_choice: { kind: "none" } },
      parsed,
    );
    expect(merged.project_id).toBe("p1");
    expect(merged.project_choice).toEqual({ kind: "none" });
    expect(merged.project_proposal?.name).toBe("通力电梯");
  });

  it("drops a proposal that tries to smuggle an id instead of a name", () => {
    const parsed = parseIssueDraftBlock(
      '<issue_draft>{"title":"T","project":{"action":"existing","id":"p1"}}</issue_draft>',
    );
    expect(parsed).not.toHaveProperty("project_proposal");
  });
});

describe("issueDraftIsCreatable", () => {
  it("requires a title and nothing else", () => {
    expect(issueDraftIsCreatable({ ...EMPTY, title: "  " })).toBe(false);
    expect(issueDraftIsCreatable({ ...EMPTY, title: "T" })).toBe(true);
    expect(issueDraftIsCreatable({ ...EMPTY, title: "T", description: "" })).toBe(true);
  });

  it("refuses a group with a sub-issue that has no title", () => {
    // The server validates every node through the root's gate and refuses the
    // whole group, so the confirm button must not be offered for one.
    expect(
      issueDraftIsCreatable({
        ...EMPTY,
        title: "T",
        children: [
          { key: "c1", title: "A", description: "", status: "todo", priority: "none" },
          { key: "c2", title: "  ", description: "", status: "todo", priority: "none" },
        ],
      }),
    ).toBe(false);
  });

  it("refuses a group larger than the server's cap", () => {
    const children = Array.from({ length: 21 }, (_, index) => ({
      key: `c${index + 1}`,
      title: `Child ${index + 1}`,
      description: "",
      status: "todo",
      priority: "none",
    }));
    expect(issueDraftIsCreatable({ ...EMPTY, title: "T", children })).toBe(false);
  });
});
