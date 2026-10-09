#!/bin/bash
set -x
export GIT_AUTHOR_NAME=验收 GIT_AUTHOR_EMAIL=qa@example.invalid GIT_COMMITTER_NAME=验收 GIT_COMMITTER_EMAIL=qa@example.invalid
S=2fe1c21b-9efb-4355-83d0-10767dcca69b
cd /tmp/dene1670/repo-local && git switch -q main && git switch -qc chat/2fe1c21b
mkdir -p docs/adr docs/evidence
echo "# ADR" > docs/adr/0042-chat-sediment-must-land-on-the-project-mainline-before-it-is-recorded-even-for-shared-directories.md
echo "- [沉淀验收](sediment-acceptance-with-a-deliberately-very-long-evidence-file-name-for-wrapping.html)" > docs/evidence/INDEX.md
git add . && git commit -qm "Chat 2fe1c21b: 长文件名 ADR 与证据索引"
CHAT=$S /tmp/dene1670/m.sh chat sediment --knowledge "adr=聊天沉淀必须先进项目主线才记录，共享目录也一样" --knowledge "evidence_index=证据索引挂上沉淀验收报告" --output table
echo "exit=$?"
echo "== history"
CHAT=$S /tmp/dene1670/m.sh chat sediment --history --output table
echo "== project memory status"
/tmp/dene1670/m.sh project memory status e1e794bb-ea60-47c9-860a-a01a5f2f9c2a --output json
