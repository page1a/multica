#!/bin/bash
# candidate CLI, isolated from this agent's own Multica identity
for v in $(env | grep -o '^MULTICA_[A-Z_]*'); do unset $v; done
export HOME=/tmp/dene1670/home
export MULTICA_SERVER_URL=http://localhost:18864
export MULTICA_TOKEN=$(python3 -c "import json;print(json.load(open('/tmp/dene1670/login.json'))['token'])")
[ -f /tmp/dene1670/ws ] && export MULTICA_WORKSPACE_ID=$(cat /tmp/dene1670/ws)
[ -n "$CHAT" ] && export MULTICA_CHAT_SESSION_ID=$CHAT
exec /tmp/dene1670/multica "$@"
