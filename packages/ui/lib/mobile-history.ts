import { useEffect, useRef } from "react"

const MARKER = "__multicaMobileOverlay"

/** Give a mobile overlay one history entry so the system back gesture closes it. */
export function useMobileOverlayHistory(
  active: boolean,
  onDismiss: () => void,
) {
  const dismissRef = useRef(onDismiss)
  dismissRef.current = onDismiss

  useEffect(() => {
    if (!active || typeof window === "undefined") return
    if (!window.matchMedia("(pointer: coarse) and (max-width: 767px)").matches) {
      return
    }

    const token = Math.random().toString(36).slice(2)
    const base = (window.history.state ?? {}) as Record<string, unknown>
    window.history.pushState({ ...base, [MARKER]: token }, "", window.location.href)
    let popped = false

    const onPopState = () => {
      const state = window.history.state as Record<string, unknown> | null
      if (state?.[MARKER] === token) return
      popped = true
      dismissRef.current()
    }
    window.addEventListener("popstate", onPopState)

    return () => {
      window.removeEventListener("popstate", onPopState)
      const state = window.history.state as Record<string, unknown> | null
      if (!popped && state?.[MARKER] === token) window.history.back()
    }
  }, [active])
}
