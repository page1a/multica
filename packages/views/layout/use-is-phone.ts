"use client";

import { useEffect, useState } from "react";
import { PHONE_MEDIA_QUERY, isPhoneViewport } from "@multica/core/paths";

/** Live `isPhoneViewport()`: narrow and touch-driven, so tablets and narrow desktop windows stay out. */
export function useIsPhone(): boolean {
  const [isPhone, setIsPhone] = useState(false);
  useEffect(() => {
    const mql = window.matchMedia(PHONE_MEDIA_QUERY);
    const onChange = () => setIsPhone(isPhoneViewport());
    onChange();
    mql.addEventListener("change", onChange);
    return () => mql.removeEventListener("change", onChange);
  }, []);
  return isPhone;
}

/** Whether `el` raises the on-screen keyboard when focused. */
export function isTextEntry(el: Element | null): boolean {
  if (!el) return false;
  if (el instanceof HTMLTextAreaElement) return !el.readOnly;
  if (el instanceof HTMLInputElement) {
    return !el.readOnly && !NON_TEXT_INPUTS.has(el.type);
  }
  return el instanceof HTMLElement && el.isContentEditable === true;
}

const NON_TEXT_INPUTS = new Set([
  "button",
  "checkbox",
  "color",
  "file",
  "hidden",
  "image",
  "radio",
  "range",
  "reset",
  "submit",
]);

/** True while focus sits in a text field — on a phone, while the keyboard is up. */
export function useTextEntryFocused(enabled: boolean): boolean {
  const [focused, setFocused] = useState(false);
  useEffect(() => {
    if (!enabled) {
      setFocused(false);
      return;
    }
    const sync = () => setFocused(isTextEntry(document.activeElement));
    sync();
    // focusout fires before the next element takes focus; read activeElement
    // a task later so tabbing between two fields does not flash the bar.
    let timer: ReturnType<typeof setTimeout> | undefined;
    const onFocusOut = () => {
      clearTimeout(timer);
      timer = setTimeout(sync, 0);
    };
    document.addEventListener("focusin", sync);
    document.addEventListener("focusout", onFocusOut);
    return () => {
      clearTimeout(timer);
      document.removeEventListener("focusin", sync);
      document.removeEventListener("focusout", onFocusOut);
    };
  }, [enabled]);
  return focused;
}
