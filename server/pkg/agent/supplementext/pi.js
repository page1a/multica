// Multica task-supplement extension for `pi -p --mode json`, loaded with -e.
// It reads the daemon's inbox (see supplement_inbox.go) and queues each input
// as Pi steering: the agent loop delivers it after the running tool calls
// finish and before the next model call, inside the same turn.
import fs from "node:fs";
import path from "node:path";

export default function (pi) {
  const dir = process.env.MULTICA_SUPPLEMENT_DIR;
  if (!dir) return;
  let streaming = false;
  const pending = new Map(); // inbox id -> text, queued but not yet consumed

  const ack = (id, ok, detail) => {
    const file = path.join(dir, id + (ok ? ".ok" : ".err"));
    try {
      fs.writeFileSync(file + ".tmp", detail || "");
      fs.renameSync(file + ".tmp", file);
    } catch {}
  };
  const textOf = (message) =>
    typeof message.content === "string"
      ? message.content
      : (message.content || [])
          .filter((c) => c.type === "text")
          .map((c) => c.text)
          .join("");

  pi.on("agent_start", () => {
    streaming = true;
  });
  pi.on("agent_end", () => {
    streaming = false;
    for (const id of pending.keys()) ack(id, false, "turn ended before delivery");
    pending.clear();
  });
  pi.on("message_start", (event) => {
    // Delivered once the loop moves the queued steering into the transcript.
    if (event.message?.role !== "user") return;
    const text = textOf(event.message);
    for (const [id, queued] of pending) {
      if (queued === text) {
        pending.delete(id);
        ack(id, true);
        break;
      }
    }
  });

  const poll = () => {
    if (!streaming) return;
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
      pending.set(id, text);
      try {
        pi.sendUserMessage(text, { deliverAs: "steer" });
      } catch (err) {
        pending.delete(id);
        ack(id, false, String(err));
      }
    }
  };
  setInterval(poll, 100).unref?.();
}
