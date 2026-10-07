import { describe, expect, it } from "vitest";
import table from "./domain-fit.cases.json";
import {
  agentDomainFit,
  agentSceneOf,
  domainScene,
  groupAgentsByFit,
  sortAgentsByDomainFit,
} from "./domain-fit";

// Domain names stand in for ids: the table is shared with the Go tests.
describe("domainScene (shared case table)", () => {
  for (const c of table.scenes) {
    it(c.name, () => {
      expect(
        domainScene({ issueDomainId: c.issue_domain, projectDomainIds: c.project_domains }),
      ).toEqual(c.want);
    });
  }

  it("drops duplicate project domains", () => {
    expect(domainScene({ projectDomainIds: ["出海", "出海", "游戏"] })).toEqual([
      "出海",
      "游戏",
    ]);
  });
});

describe("agentDomainFit (shared case table)", () => {
  for (const c of table.cases) {
    it(c.name, () => {
      expect(agentDomainFit(c.scene, { domain_id: c.agent.domain })).toBe(c.fit);
    });
  }
});

describe("sortAgentsByDomainFit", () => {
  const agents = [
    { name: "孙悟空游戏", domain_id: "游戏" },
    { name: "布尔玛", domain_id: null },
    { name: "孙悟空出海", domain_id: "出海" },
    { name: "孙悟空" },
  ];

  it("puts the fitting specialisation before its base role", () => {
    expect(sortAgentsByDomainFit(agents, ["出海"]).map((a) => a.name)).toEqual([
      "孙悟空出海",
      "布尔玛",
      "孙悟空",
      "孙悟空游戏",
    ]);
  });

  it("a generic scene puts base roles first", () => {
    expect(sortAgentsByDomainFit(agents, []).map((a) => a.name)).toEqual([
      "布尔玛",
      "孙悟空",
      "孙悟空游戏",
      "孙悟空出海",
    ]);
  });
});

const agent = (id: string, domain?: string) => ({ id, domain_id: domain });
const project = (id: string, domains: string[]) => ({ id, title: id, domain_ids: domains });

describe("agentSceneOf", () => {
  it("no project is no scene", () => {
    expect(agentSceneOf([])).toBeNull();
  });

  it("unions project domains, the issue's own domain wins", () => {
    const ps = [project("a", ["d1"]), project("b", ["d2", "d1"])];
    expect(agentSceneOf(ps)?.domains).toEqual(["d1", "d2"]);
    expect(agentSceneOf(ps, "d2")?.domains).toEqual(["d2"]);
  });

  it("a project with no domain is a generic scene, not no scene", () => {
    expect(agentSceneOf([project("m", [])])?.domains).toEqual([]);
  });
});

describe("groupAgentsByFit", () => {
  const agents = [agent("base"), agent("game", "d-game"), agent("out", "d-out"), agent("base2")];

  it("domain scene: fit, then base roles, then other domains, order kept", () => {
    const scene = agentSceneOf([project("tarot", ["d-out"])])!;
    expect(groupAgentsByFit(agents, scene).map((g) => [g.fit, g.items.map((a) => a.id)])).toEqual([
      ["match", ["out"]],
      ["generic", ["base", "base2"]],
      ["other", ["game"]],
    ]);
  });

  it("generic scene: base roles fit, specialisations last, no empty group", () => {
    const scene = agentSceneOf([project("m", [])])!;
    expect(groupAgentsByFit(agents, scene).map((g) => g.fit)).toEqual(["match", "other"]);
  });
});
