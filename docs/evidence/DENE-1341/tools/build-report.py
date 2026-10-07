#!/usr/bin/env python3
"""DENE-1341 证据报告：把 raw/before 与 raw/after 的截图压成 webp，生成 report.html。

用法（仓库根目录，两轮 capture.mjs 都跑完之后）：
  python3 docs/evidence/DENE-1341/tools/build-report.py

- 390：每页前后对照都进报告（缩到 1x，webp）。
- 768 / 1280：逐像素比对前后，只有真的变了的页才进报告，其余列为「无变化」。
"""
import html
import json
import subprocess
from pathlib import Path

from PIL import Image, ImageChops

HERE = Path(__file__).resolve().parent
ROOT = HERE.parent
RAW = ROOT / "raw"
IMG = ROOT / "img"
IMG.mkdir(exist_ok=True)

LABELS = {
    "home": "首页", "inbox": "收件箱", "my-issues": "我的任务", "issues": "任务列表",
    "issue-detail": "任务详情（父任务）", "issue-child": "任务详情（子任务）", "chat": "聊天列表",
    "chat-session": "聊天会话", "projects": "项目列表", "project-detail": "项目详情",
    "agents": "智能体列表", "agent-detail": "智能体详情", "squads": "小队列表", "squad-detail": "小队详情",
    "autopilots": "自动化列表", "autopilot-detail": "自动化详情", "runtimes": "运行时列表",
    "runtime-detail": "运行时详情", "skills": "技能", "usage": "统计", "member-detail": "成员详情",
    "linked": "连通工作区",
}
SETTINGS = {
    "profile": "个人资料", "preferences": "偏好", "notifications": "通知", "tokens": "令牌",
    "workspace": "工作区", "members": "成员", "labels": "标签", "issue-statuses": "任务状态",
    "properties": "属性", "routing": "路由", "wakeups": "唤醒", "quick-actions": "快捷操作",
    "shortcuts": "快捷键", "code": "代码", "connections": "连接", "apps": "应用",
    "agent-permissions": "智能体权限", "workspace-links": "工作区连通", "project-sharing": "项目共享",
    "config-transfer": "配置迁移", "billing": "账单",
}
for key, label in SETTINGS.items():
    LABELS[f"settings-{key}"] = f"设置 · {label}"

# 修了什么，一句话；没写的页是走查过、没发现要改的。
NOTES = {
    "issue-detail": "顶栏改成手机紧凑版：返回 + 编号，其余操作收进「⋯」；排队徽标不再竖排。",
    "issue-child": "同上；面包屑父任务不再被压成一个字母。",
    "project-detail": "面包屑项目名改成省略号截断；列表底部给聊天浮钮留出空位。",
    "squad-detail": "成员行的「智能体 / 队长 / 空闲」不再一字一行；面包屑小队名截断。",
    "autopilot-detail": "面包屑自动化名不再被压成一个字。",
    "chat-session": "会话标题过长时出省略号，不再硬切成「Multic」。",
    "runtime-detail": "运行时表格窄屏按比例分给名称和健康度，名称不再被压成「k…」。",
    "settings-members": "设置页标题下的工作区标签可截断，不再钻到「邀请成员」按钮下面。",
}
QUOTA_NOTE = "底部额度条收成一行：每家只显示最紧的剩余比例，点开看全部窗口和切换来源；浮钮不再盖住它。"


def webp(src: Path, dst: Path, width: int) -> None:
    im = Image.open(src).convert("RGB")
    if im.width != width:
        im = im.resize((width, round(im.height * width / im.width)), Image.LANCZOS)
    tmp = dst.with_suffix(".tmp.png")
    im.save(tmp)
    subprocess.run(["cwebp", "-quiet", "-q", "78", str(tmp), "-o", str(dst)], check=True)
    tmp.unlink()
    assert dst.stat().st_size <= 300 * 1024, dst


def changed_ratio(a: Path, b: Path) -> float:
    ia, ib = Image.open(a).convert("L"), Image.open(b).convert("L")
    if ia.size != ib.size:
        return 1.0
    diff = ImageChops.difference(ia, ib).point(lambda v: 255 if v > 24 else 0)
    hist = diff.histogram()
    return hist[255] / (ia.width * ia.height)


def main() -> None:
    names = [n for n in LABELS if (RAW / "before" / f"{n}-390.png").exists()]
    sections = []
    for name in names:
        before = RAW / "before" / f"{name}-390.png"
        after = RAW / "after" / f"{name}-390.png"
        if not after.exists():
            continue
        webp(before, IMG / f"{name}-390-before.webp", 390)
        webp(after, IMG / f"{name}-390-after.webp", 390)
        note = NOTES.get(name, "走查无排版问题。")
        sections.append(
            f'<section><h3>{html.escape(LABELS[name])}</h3><p>{html.escape(note)}</p>'
            f'<div class="pair"><figure><img loading="lazy" src="img/{name}-390-before.webp" alt="修改前"><figcaption>修改前</figcaption></figure>'
            f'<figure><img loading="lazy" src="img/{name}-390-after.webp" alt="修改后"><figcaption>修改后</figcaption></figure></div></section>'
        )

    regress_rows, regress_pairs = [], []
    for width in (768, 1280):
        for name in names:
            before = RAW / "before" / f"{name}-{width}.png"
            after = RAW / "after" / f"{name}-{width}.png"
            if not (before.exists() and after.exists()):
                continue
            ratio = changed_ratio(before, after)
            regress_rows.append((name, width, ratio))
            if ratio > 0.01:
                webp(before, IMG / f"{name}-{width}-before.webp", min(width, 960))
                webp(after, IMG / f"{name}-{width}-after.webp", min(width, 960))
                regress_pairs.append(
                    f'<section><h3>{html.escape(LABELS[name])} · {width}</h3><p>变化像素 {ratio:.1%}</p>'
                    f'<div class="pair wide"><figure><img loading="lazy" src="img/{name}-{width}-before.webp" alt="修改前"><figcaption>修改前</figcaption></figure>'
                    f'<figure><img loading="lazy" src="img/{name}-{width}-after.webp" alt="修改后"><figcaption>修改后</figcaption></figure></div></section>'
                )
    table = "".join(
        f"<tr><td>{html.escape(LABELS[n])}</td><td>{w}</td><td>{r:.2%}</td></tr>" for n, w, r in regress_rows
    )
    (ROOT / "regression.json").write_text(
        json.dumps([{"page": n, "width": w, "changed": round(r, 4)} for n, w, r in regress_rows], indent=1)
    )

    page = f"""<!doctype html>
<html lang="zh-CN"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>DENE-1341 手机端有数据走查</title>
<style>
body{{margin:0;font:14px/1.6 -apple-system,BlinkMacSystemFont,"PingFang SC",sans-serif;color:#18181b;background:#fafafa}}
main{{max-width:960px;margin:0 auto;padding:24px 16px 64px}}
h1{{font-size:20px;margin:0 0 4px}} h2{{font-size:16px;margin:32px 0 8px}} h3{{font-size:14px;margin:0}}
p{{margin:4px 0 8px;color:#52525b}}
section{{border-top:1px solid #e4e4e7;padding:16px 0}}
.pair{{display:grid;grid-template-columns:repeat(2,minmax(0,1fr));gap:12px}}
.pair.wide{{grid-template-columns:minmax(0,1fr)}}
figure{{margin:0}} img{{display:block;width:100%;max-width:390px;border:1px solid #e4e4e7;border-radius:8px}}
.wide img{{max-width:100%}}
figcaption{{font-size:12px;color:#71717a;margin-top:4px}}
table{{border-collapse:collapse;width:100%;font-size:12px}} td,th{{border-bottom:1px solid #e4e4e7;padding:4px 6px;text-align:left}}
details summary{{cursor:pointer;color:#52525b}}
</style></head><body><main>
<h1>DENE-1341 手机端全站有数据走查</h1>
<p>同一份造数（长名字的智能体、项目、任务、小队、聊天、额度快照），390 / 768 / 1280 三档截图。下面每页左边修改前、右边修改后，均为 390 宽。</p>
<p>{html.escape(QUOTA_NOTE)} 所有列表在手机上底部多留一个浮钮的高度，滚到底不再被盖住。</p>
<h2>390 前后对照</h2>
{''.join(sections)}
<h2>768 / 1280 回归</h2>
<p>逐像素比对修改前后；变化超过 1% 的页面附截图，其余视为无变化（时间戳类文字会带来零点几的差异）。</p>
{''.join(regress_pairs) or '<p>没有页面变化超过 1%。</p>'}
<details><summary>全部比对数据</summary><table><tr><th>页面</th><th>宽度</th><th>变化像素</th></tr>{table}</table></details>
</main></body></html>
"""
    (ROOT / "report.html").write_text(page)
    print(f"390 pairs: {len(sections)}, wide changes: {len(regress_pairs)}")


if __name__ == "__main__":
    main()
