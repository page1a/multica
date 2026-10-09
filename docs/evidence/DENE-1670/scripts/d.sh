#!/bin/bash
set -x
export GIT_AUTHOR_NAME=验收 GIT_AUTHOR_EMAIL=qa@example.invalid GIT_COMMITTER_NAME=验收 GIT_COMMITTER_EMAIL=qa@example.invalid
cd /tmp/dene1670/repo-local && git switch -q main && git switch -qc sed-3
echo "未核对：收口声明写了知识，但没读到交付文件" >> CONTEXT.md
git add . && git commit -qm "SED-3: 补「未核对」词条"
echo "收口条验收：交付文件可读" > ev3.md
/tmp/dene1670/m.sh issue close SED-3 --outcome done --no-code "本地测试仓库，无 PR" --summary "读到交付文件的收口" --evidence-file ev3.md --knowledge "context=补了「未核对」词条" --output json
echo "exit=$?"
mkdir -p /tmp/dene1670/nogit && cd /tmp/dene1670/nogit && echo "收口条验收：不在 git 里" > ev4.md
/tmp/dene1670/m.sh issue close SED-4 --outcome done --no-code "本地测试，不在 git 目录" --summary "没读到交付文件的收口" --evidence-file ev4.md --knowledge "agents=声明改了 AGENTS.md" --output json
echo "exit=$?"
