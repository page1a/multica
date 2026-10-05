// Multica task-supplement plugin for `opencode run` (OpenCode 1.x), loaded
// through OPENCODE_CONFIG_CONTENT. It reads the daemon's inbox (see
// supplement_inbox.go) and adds each input to the root session as a real user
// message. A busy session loop picks it up at its next step, so the model reads
// it inside the same turn without interrupting the running tool.
import fs from "node:fs";
import path from "node:path";

export const MulticaSupplementPlugin = async ({ client }) => {
  const dir = process.env.MULTICA_SUPPLEMENT_DIR;
  if (!dir) return {};
  const children = new Set();
  let root;
  let busy = false;
  const claimed = new Map(); // inbox id -> text, sent but not yet stored
  const sent = new Map(); // user message id -> inbox id, stored but not yet answered

  const ack = (id, ok, detail) => {
    const file = path.join(dir, id + (ok ? ".ok" : ".err"));
    try {
      fs.writeFileSync(file + ".tmp", detail || "");
      fs.renameSync(file + ".tmp", file);
    } catch {}
  };
  const failAll = (why) => {
    for (const id of claimed.keys()) ack(id, false, why);
    for (const id of sent.values()) ack(id, false, why);
    claimed.clear();
    sent.clear();
  };

  const poll = async () => {
    if (!root || !busy) return;
    let names;
    try {
      names = fs.readdirSync(dir).filter((n) => n.endsWith(".txt")).sort();
    } catch {
      return;
    }
    for (const name of names) {
      const id = name.slice(0, -4);
      const file = path.join(dir, id + ".claimed");
      // Losing this rename means the daemon withdrew the input.
      try {
        fs.renameSync(path.join(dir, name), file);
      } catch {
        continue;
      }
      const text = fs.readFileSync(file, "utf8");
      claimed.set(id, text);
      try {
        const res = await client.session.promptAsync({ path: { id: root }, body: { parts: [{ type: "text", text }] } });
        if (res?.error) throw new Error(JSON.stringify(res.error));
      } catch (err) {
        claimed.delete(id);
        ack(id, false, String(err));
      }
    }
  };
  let polling = false;
  const timer = setInterval(() => {
    if (polling) return;
    polling = true;
    poll().finally(() => {
      polling = false;
    });
  }, 100);
  timer.unref?.();

  return {
    event: async ({ event }) => {
      const p = event.properties || {};
      switch (event.type) {
        case "session.created":
          // Subagent sessions carry a parent; only the run's root session steers.
          if (p.info?.parentID) children.add(p.info.id);
          break;
        case "session.status":
          if (children.has(p.sessionID)) break;
          if (!root && p.status?.type === "busy") root = p.sessionID;
          if (p.sessionID !== root) break;
          busy = p.status?.type !== "idle";
          if (!busy) failAll("turn ended before delivery");
          break;
        case "message.part.updated": {
          const part = p.part;
          if (part?.type !== "text" || part.sessionID !== root) break;
          for (const [id, text] of claimed) {
            if (text === part.text) {
              claimed.delete(id);
              sent.set(part.messageID, id);
              break;
            }
          }
          break;
        }
        case "message.updated": {
          // Delivered once the model starts answering that user message.
          const info = p.info;
          if (info?.role !== "assistant" || info.sessionID !== root) break;
          const id = sent.get(info.parentID);
          if (id !== undefined) {
            sent.delete(info.parentID);
            ack(id, true);
          }
          break;
        }
      }
    },
  };
};
