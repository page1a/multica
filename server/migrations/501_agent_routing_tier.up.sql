-- routing_tier is the seat's strength label: 最强 / 强 / 中 / 弱, stored as the
-- tier key routing uses (strongest / strong / medium / weak). NULL means the
-- seat carries no tier and routing will not consider it a candidate.
--
-- It is a human-set tag rather than something derived from `model`, because
-- strength is not a property of the model id alone: the same model at a
-- different thinking_level is a different rung, and two seats on one model may
-- deliberately sit on different rungs.
ALTER TABLE agent
    ADD COLUMN IF NOT EXISTS routing_tier TEXT;
