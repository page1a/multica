#!/bin/bash
set -x
cd /tmp/dene1670 && rm -rf repo-local && mkdir repo-local && cd repo-local
export GIT_AUTHOR_NAME=验收 GIT_AUTHOR_EMAIL=qa@example.invalid GIT_COMMITTER_NAME=验收 GIT_COMMITTER_EMAIL=qa@example.invalid
git init -q -b main && echo "# 测试项目" > AGENTS.md && git add . && git commit -qm init
git switch -qc chat/80f77278
echo "- 新规则：沉淀走主线" >> AGENTS.md
echo "沉淀记录：聊天把结论合进主线留下的一笔" > CONTEXT.md
git add . && git commit -qm "Chat 80f77278: 补规则和词条"
git log --oneline --all --graph
CHAT=80f77278-d1d7-4c85-927d-374f61c0cf29 /tmp/dene1670/m.sh chat sediment --knowledge "agents=加了沉淀走主线的规则" --knowledge "context=加了「沉淀记录」词条" --output json
echo "exit=$?"
git log --oneline --graph main
git branch --show-current
