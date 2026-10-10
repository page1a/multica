"use client";

import { useEffect } from "react";
import { useRouter } from "next/navigation";
import { useRequiredWorkspaceSlug, workspaceLandingPath } from "@multica/core/paths";

// The bare workspace URL (`/` redirects here) has no page of its own: it hands
// over to the landing page, which only the browser can pick — Chat on a phone,
// Issues elsewhere.
export default function WorkspaceRootPage() {
  const slug = useRequiredWorkspaceSlug();
  const router = useRouter();
  useEffect(() => {
    router.replace(workspaceLandingPath(slug));
  }, [router, slug]);
  return null;
}
