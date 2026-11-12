import { useEffect, useState } from "react";

export type ThemePref = "light" | "dark" | "system";

const KEY = "athens.theme";

function readPref(): ThemePref {
  try {
    const saved = localStorage.getItem(KEY);
    if (saved === "light" || saved === "dark" || saved === "system") return saved;
  } catch {
    /* storage unavailable */
  }
  return "system";
}

const media = () => window.matchMedia("(prefers-color-scheme: dark)");

function apply(pref: ThemePref) {
  const dark = pref === "dark" || (pref === "system" && media().matches);
  document.documentElement.dataset.theme = dark ? "dark" : "light";
}

/** Theme preference (light, dark, or follow the OS), persisted per browser. */
export function useTheme(): [ThemePref, (p: ThemePref) => void] {
  const [pref, setPref] = useState<ThemePref>(readPref);
  useEffect(() => {
    apply(pref);
    try {
      localStorage.setItem(KEY, pref);
    } catch {
      /* storage unavailable */
    }
    if (pref !== "system") return;
    const m = media();
    const onChange = () => apply("system");
    m.addEventListener("change", onChange);
    return () => m.removeEventListener("change", onChange);
  }, [pref]);
  return [pref, setPref];
}
