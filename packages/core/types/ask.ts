export type AskOption = { id: string; label: string; recommended?: boolean };
export type AskQuestion = { text: string; options: AskOption[] };
export type Ask = {
  id: string;
  workspace_id: string;
  issue_id?: string;
  asker_type: "member" | "agent";
  asker_id: string;
  title: string;
  questions: AskQuestion[];
  answers?: Record<string, string>;
  mode: "needs_you" | "side_question";
  status: "open" | "answered" | "cancelled";
  created_at: string;
  answered_at?: string;
};
export type CreateAskRequest = Pick<Ask, "title" | "questions"> & { issue_id?: string; mode?: Ask["mode"] };
export type AnswerAskRequest = { answers: Record<string, string> };
