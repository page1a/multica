/**
 * Where a user is told to get the Multica CLI.
 *
 * This fork builds its own CLI and its own self-host images, so every install
 * hint the product prints has to point at the fork. Three surfaces used to
 * carry their own copy of the upstream one-liner — the landing page, the
 * onboarding step and the "add a computer" dialog — and a self-hosted instance
 * therefore handed people the upstream binary (DENE-420). They all read this
 * constant now, so there is one place to change.
 */
export const CLI_REPO_SLUG = "jeff-kunkun/multica";
export const CLI_REPO_BRANCH = "kun";

export const CLI_INSTALL_SCRIPT_URL = `https://raw.githubusercontent.com/${CLI_REPO_SLUG}/${CLI_REPO_BRANCH}/scripts/install.sh`;

/** The copy-and-paste one-liner shown wherever we ask someone to install the CLI. */
export const CLI_INSTALL_COMMAND = `curl -fsSL ${CLI_INSTALL_SCRIPT_URL} | bash`;

export const CLI_INSTALL_SCRIPT_PS1_URL = `https://raw.githubusercontent.com/${CLI_REPO_SLUG}/${CLI_REPO_BRANCH}/scripts/install.ps1`;

/** The Windows PowerShell counterpart of {@link CLI_INSTALL_COMMAND}. */
export const CLI_INSTALL_COMMAND_WINDOWS = `irm ${CLI_INSTALL_SCRIPT_PS1_URL} | iex`;
