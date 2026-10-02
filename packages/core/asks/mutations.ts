import { useMutation, useQueryClient } from "@tanstack/react-query";
import { api } from "../api";
import { useWorkspaceId } from "../hooks";
import { askKeys } from "./queries";

export function useAnswerAsk() {
  const qc = useQueryClient();
  const wsId = useWorkspaceId();
  return useMutation({
    mutationFn: ({ id, answers }: { id: string; answers: Record<string, string> }) => api.answerAsk(id, { answers }),
    onSettled: (_data, _error, variables) => {
      qc.invalidateQueries({ queryKey: askKeys.all(wsId) });
      qc.invalidateQueries({ queryKey: askKeys.detail(wsId, variables.id) });
      qc.invalidateQueries({ queryKey: ["inbox", wsId] });
    },
  });
}
