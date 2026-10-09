#!/bin/bash
set -x
DB=postgres://multica:multica@localhost:5432/multica_dene_1670_b7a894d98087_784
S=9c430eb4-8918-48c6-a619-16850de8e424
cd /tmp/dene1670 && rm -rf remote.git repo-remote seed
export GIT_AUTHOR_NAME=验收 GIT_AUTHOR_EMAIL=qa@example.invalid GIT_COMMITTER_NAME=验收 GIT_COMMITTER_EMAIL=qa@example.invalid
git init -q --bare -b main remote.git
git clone -q remote.git seed 2>/dev/null; cd seed && git switch -qc main && echo "# 远端测试项目" > AGENTS.md && mkdir -p docs && echo "# 文档索引" > docs/README.md && git add . && git commit -qm init && git push -q origin main && cd ..
git clone -q remote.git repo-remote && cd repo-remote
echo "- 远端规则：推送后才算沉淀" >> AGENTS.md
echo "- [证据索引](evidence/INDEX.md)" >> docs/README.md
git add . && git commit -qm "Chat 9c430eb4: 补规则和文档索引"
echo "== B1: 已提交未推送"
psql $DB -Atc "select count(*) from knowledge_sediment where chat_session_id='$S'"
CHAT=$S /tmp/dene1670/m.sh chat sediment --knowledge "agents=推送后才算沉淀" --knowledge "docs_index=文档索引挂上证据索引" --output json
echo "exit=$?"
psql $DB -Atc "select count(*) from knowledge_sediment where chat_session_id='$S'"
git ls-remote origin main
echo "== B2: 推送到远端 main 后重试"
git push -q origin main
CHAT=$S /tmp/dene1670/m.sh chat sediment --knowledge "agents=推送后才算沉淀" --knowledge "docs_index=文档索引挂上证据索引" --output json
echo "exit=$?"
psql $DB -Atc "select count(*), max(mainline), max(commits::text) from knowledge_sediment where chat_session_id='$S'"
git rev-parse HEAD
