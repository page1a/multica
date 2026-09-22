-- code_decision is where this run's code lives, decided once on the server by
-- internal/coderesolve at claim time and stored so every later read returns the
-- same answer: the daemon gets it on the claim, the UI gets it from the task
-- row, and neither re-derives the rule from the resource list.
--
-- Stored rather than recomputed on read because the inputs move: a project's
-- resources can be reordered or unbound after a run started, and a task's page
-- must say where the run actually went, not where a run starting now would go.
--
-- JSONB holding the closed sum type's wire form — always a `kind`, including
-- kind="unresolvable" with a code. NULL means only "claimed by a server that
-- predates this column", which is a different thing from a run whose code
-- source could not be resolved.
ALTER TABLE agent_task_queue
    ADD COLUMN IF NOT EXISTS code_decision JSONB;
