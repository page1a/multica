import { createContext, useContext } from "react";
import type { AgentScene } from "@multica/core/agents";

/**
 * The scene of the surface an editor sits in (DENE-1477). Issue detail, the
 * create dialogs and chat provide it, so a nested @ mention list ranks agents
 * the same way their pickers do. Null = no project in play.
 */
const AgentSceneContext = createContext<AgentScene | null>(null);

export const AgentSceneProvider = AgentSceneContext.Provider;

export function useContextAgentScene(): AgentScene | null {
  return useContext(AgentSceneContext);
}
