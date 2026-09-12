import { useEffect, useState } from "react"
import { MoonIcon, SunIcon } from "lucide-react"

import { useTheme } from "@/components/theme-provider"
import { Button } from "@/components/ui/button"

function useSystemDark() {
  const [systemDark, setSystemDark] = useState(() =>
    typeof window !== "undefined" &&
    typeof window.matchMedia === "function"
      ? window.matchMedia("(prefers-color-scheme: dark)").matches
      : true
  )

  useEffect(() => {
    const mql = window.matchMedia("(prefers-color-scheme: dark)")
    const onChange = (event: MediaQueryListEvent) =>
      setSystemDark(event.matches)
    mql.addEventListener("change", onChange)
    setSystemDark(mql.matches)
    return () => mql.removeEventListener("change", onChange)
  }, [])

  return systemDark
}

export function ThemeToggle() {
  const { theme, setTheme } = useTheme()
  const systemDark = useSystemDark()
  const resolved = theme === "system" ? (systemDark ? "dark" : "light") : theme
  const dark = resolved === "dark"
  return (
    <Button
      variant="ghost"
      size="icon"
      type="button"
      aria-label={dark ? "Switch to light theme" : "Switch to dark theme"}
      onClick={() => setTheme(dark ? "light" : "dark")}
    >
      {dark ? (
        <SunIcon data-icon="inline-start" />
      ) : (
        <MoonIcon data-icon="inline-start" />
      )}
    </Button>
  )
}
