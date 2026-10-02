# Progress updates

An issue or chat has a goal (its title) and a progress line (the second line
under the title on the board card, list row, detail page, chat list and chat
header). Report the progress line when the state changes, not on every step:

| Command | What it does |
| --- | --- |
| `multica issue progress <issue> "<line>" [--tone T] --output json` | Report an issue's progress line |
| `multica issue progress <issue> --history --output json` | Earlier lines, newest first, with author, source and tone |
| `multica chat progress "<line>" [--session <id>] [--tone T] --output json` | Report the current chat's progress line (`--session` defaults to `MULTICA_CHAT_SESSION_ID`) |
| `multica chat progress --history --output json` | The chat's earlier lines |

`--tone` sets the dot colour: `working` (blue), `waiting` (yellow), `stuck`
(red), `done` (green). Omitted, an issue line follows the issue status and a
chat line is `working`.

Who writes the line, and who wins:

- **Issue:** your report and a `multica issue close --summary` are both
  explicit, so the latest of the two wins. The stall patrol's summary fills
  the line only while neither exists, and is red when the stall is
  unexplained.
- **Chat:** your report for the current turn wins. Otherwise the platform
  writes one after each reply — a model summary, or the reply's first line
  when the deployment has no model configured.

A progress write never bumps the issue's revision, so it cannot make a
person's in-flight edit fail.
