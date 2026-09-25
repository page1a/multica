-- DENE-854: a following specialisation now also takes its base role's
-- custom_env, custom_args and mcp_config (same owner only). The copy is kept by
-- SyncInheritedAgentRuntimeProfiles on every base-role edit; this brings the
-- rows that already follow up to date once, instead of waiting for that edit.
UPDATE agent AS child
SET custom_env = parent.custom_env,
    custom_args = parent.custom_args,
    mcp_config = parent.mcp_config,
    updated_at = now()
FROM agent AS parent
WHERE child.parent_agent_id = parent.id
  AND child.runtime_inherited
  AND child.archived_at IS NULL
  AND child.owner_id = parent.owner_id
  AND (child.custom_env IS DISTINCT FROM parent.custom_env
    OR child.custom_args IS DISTINCT FROM parent.custom_args
    OR child.mcp_config IS DISTINCT FROM parent.mcp_config);
