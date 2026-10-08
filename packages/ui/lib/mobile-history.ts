import { useEffect, useRef } from "react"

const MARKER = "__multicaMobileOverlay"

// An entry whose release is deferred by one task. A re-run of the effect
// (React StrictMode, or one overlay handing over to the next) adopts it
// instead of pushing a second entry: history.back() settles asynchronously,
// and its popstate would otherwise land on the new overlay and close it.
let releasing: { token: string; timer: ReturnType<typeof setTimeout> } | null = null

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

    let token: string
    const held = releasing
    releasing = null
    if (held) clearTimeout(held.timer)
    const current = window.history.state as Record<string, unknown> | null
    if (held && current?.[MARKER] === held.token) {
      token = held.token
    } else {
      token = Math.random().toString(36).slice(2)
      const base = (window.history.state ?? {}) as Record<string, unknown>
      window.history.pushState({ ...base, [MARKER]: token }, "", window.location.href)
    }
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
      if (popped) return
      const timer = setTimeout(() => {
        releasing = null
        const state = window.history.state as Record<string, unknown> | null
        if (state?.[MARKER] === token) window.history.back()
      }, 0)
      releasing = { token, timer }
    }
  }, [active])
}
