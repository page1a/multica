import { describe, expect, it } from "vitest";
import type { Agent, Project } from "@multica/core/types";
import { groupAgentsForPicker } from "./agent-picker-groups";

const agent = (id: string, owner: string, domain?: string) =>
  ({ id, name: id, owner_id: owner, domain_id: domain }) as Agent;
const project = (id: string, domains: string[]) =>
  ({ id, title: id, domain_ids: domains }) as Project;

const agents = [
  agent("base", "me"),
  agent("out-a", "other", "d-out"),
  agent("media-a", "me", "d-media"),
  agent("game-a", "me", "d-game"),
];
const ids = (g: { agents: Agent[] }) => g.agents.map((a) => a.id);

describe("groupAgentsForPicker", () => {
  it("no project keeps mine / others", () => {
    const g = groupAgentsForPicker({ agents, userId: "me", projects: [] });
    expect(g.map((x) => x.kind)).toEqual(["mine", "others"]);
    expect(ids(g[0]!)).toEqual(["base", "media-a", "game-a"]);
  });

  it("domain project lists its specialisations first, keeping order", () => {
    const g = groupAgentsForPicker({
      agents,
      userId: "me",
      projects: [project("tarot", ["d-out", "d-media"])],
    });
    expect(g.map((x) => x.kind)).toEqual(["match", "generic", "other"]);
    expect(ids(g[0]!)).toEqual(["out-a", "media-a"]);
    expect(ids(g[1]!)).toEqual(["base"]);
    expect(ids(g[2]!)).toEqual(["game-a"]);
  });

  it("generic project lists base roles first", () => {
    const g = groupAgentsForPicker({ agents, userId: "me", projects: [project("m", [])] });
    expect(g.map((x) => x.kind)).toEqual(["match", "other"]);
    expect(ids(g[0]!)).toEqual(["base"]);
    expect(ids(g[1]!)).toEqual(["out-a", "media-a", "game-a"]);
  });

  it("several projects use the union of their domains", () => {
    const g = groupAgentsForPicker({
      agents,
      userId: "me",
      projects: [project("a", ["d-out"]), project("b", ["d-game"])],
    });
    expect(ids(g[0]!)).toEqual(["out-a", "game-a"]);
  });

  it("search runs over everyone and still groups, dropping empty groups", () => {
    const g = groupAgentsForPicker({
      agents,
      userId: "me",
      projects: [project("tarot", ["d-out"])],
      query: "media",
    });
    expect(g.map((x) => x.kind)).toEqual(["other"]);
    expect(ids(g[0]!)).toEqual(["media-a"]);
  });
});
