"use client";

import { useEffect, useRef, useState } from "react";
import { clientErrorMessage } from "@multica/core/api";
import { useAuthStore } from "@multica/core/auth";
import { provisionedDirectoryShouldBeRemoved } from "@multica/core/issue-drafts";
import { useCreateProject } from "@multica/core/projects/mutations";
import { useT } from "../../i18n";
import { isDesktopShell } from "../../platform/local-directory";
import { useLocalDaemonStatus } from "../../platform/use-local-daemon-status";
import {
  discardProvisionedDirectory,
  initialDirectoryDraft,
  prepareNewProjectDirectory,
  readBusinessProjectRoot,
  sanitizeProjectDirectoryName,
  writeBusinessProjectRoot,
  type NewProjectDirectoryDraft,
} from "../new-project-directory";
import { NewProjectActions, NewProjectFields } from "./new-project-fields";

/**
 * Create a project from inside the picker and hand its id back. The folder,
 * when one is made, is removed again if the server refuses the project.
 */
export function NewProjectCreateForm({
  initialName,
  onCreated,
  onCancel,
}: {
  initialName: string;
  onCreated: (projectId: string) => void;
  onCancel: () => void;
}) {
  const { t } = useT("projects");
  const userId = useAuthStore((state) => state.user?.id ?? "");
  const daemon = useLocalDaemonStatus();
  const createProject = useCreateProject();
  const [name, setName] = useState(initialName);
  const [icon, setIcon] = useState("");
  const [directory, setDirectory] = useState<NewProjectDirectoryDraft>(() =>
    initialDirectoryDraft({
      root: readBusinessProjectRoot(userId, daemon.daemonId ?? ""),
      dirName: sanitizeProjectDirectoryName(initialName) || initialName.trim(),
      available: false,
    }),
  );
  const [error, setError] = useState<string | null>(null);
  const [pending, setPending] = useState(false);
  const seeded = useRef(false);

  useEffect(() => {
    if (seeded.current || !daemon.daemonId) return;
    seeded.current = true;
    setDirectory(
      initialDirectoryDraft({
        root: readBusinessProjectRoot(userId, daemon.daemonId),
        dirName: sanitizeProjectDirectoryName(initialName) || initialName.trim(),
        available: isDesktopShell() && daemon.running,
      }),
    );
  }, [daemon.daemonId, daemon.running, initialName, userId]);

  const directoryMessage = (
    reason: "unavailable" | "offline" | "bad_name" | "exists" | "failed",
  ) => {
    switch (reason) {
      case "unavailable":
        return t(($) => $.new_project.directory_unavailable);
      case "offline":
        return t(($) => $.new_project.directory_offline);
      case "bad_name":
        return t(($) => $.new_project.directory_bad_name);
      case "exists":
        return t(($) => $.new_project.directory_exists);
      case "failed":
        return t(($) => $.new_project.directory_failed);
    }
  };

  const submit = async () => {
    const title = name.trim();
    if (!title || pending) return;
    setPending(true);
    setError(null);
    if (daemon.daemonId) writeBusinessProjectRoot(userId, daemon.daemonId, directory.root);
    const prepared = await prepareNewProjectDirectory({
      draft: directory,
      daemonId: daemon.daemonId,
      title,
    });
    if (!prepared.ok) {
      setPending(false);
      setError(directoryMessage(prepared.reason));
      return;
    }
    try {
      const project = await createProject.mutateAsync({
        title,
        ...(icon.trim() ? { icon: icon.trim() } : {}),
        status: "planned",
        ...(prepared.resource
          ? {
              resources: [
                { resource_type: "local_directory" as const, resource_ref: prepared.resource },
              ],
            }
          : {}),
      });
      onCreated(project.id);
    } catch (err) {
      if (
        prepared.createdPath &&
        provisionedDirectoryShouldBeRemoved(err)
      ) {
        await discardProvisionedDirectory(prepared.createdPath, prepared.root);
      }
      setError(clientErrorMessage(err) ?? t(($) => $.new_project.failed));
      setPending(false);
    }
  };

  return (
    <div>
      <NewProjectFields
        name={name}
        onNameChange={setName}
        icon={icon}
        onIconChange={setIcon}
        directory={directory}
        onDirectoryChange={(next) => {
          setDirectory(next);
          if (daemon.daemonId) writeBusinessProjectRoot(userId, daemon.daemonId, next.root);
        }}
        directoryAvailable={isDesktopShell()}
        disabled={pending}
      />
      {error ? (
        <p role="alert" className="px-1 pb-1 text-caption text-destructive">
          {error}
        </p>
      ) : null}
      <NewProjectActions
        onBack={onCancel}
        onSubmit={() => void submit()}
        pending={pending}
        submitLabel={t(($) => $.new_project.submit)}
      />
    </div>
  );
}

