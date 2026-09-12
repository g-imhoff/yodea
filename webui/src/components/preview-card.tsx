import { useState } from "react"
import { StarIcon } from "lucide-react"

import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import {
  Card,
  CardAction,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card"

export interface PreviewItem {
  label: string
  title: string
  description: string
  href: string
  badge?: string
}

export function PreviewCard({
  item,
  favorite,
  onToggleFavorite,
}: {
  item: PreviewItem
  favorite: boolean
  onToggleFavorite: (label: string) => void | Promise<unknown>
}) {
  // In-flight guard: the server toggle is async, so ignore re-activation
  // while a toggle is pending. Tracking lives here (not in favorites.ts)
  // so the button can disable itself without dashboard changes.
  const [pending, setPending] = useState(false)
  const handleToggle = () => {
    if (pending) return
    setPending(true)
    void Promise.resolve()
      .then(() => onToggleFavorite(item.label))
      .catch(() => undefined)
      .finally(() => setPending(false))
  }
  return (
    <Card>
      <CardHeader>
        <CardTitle>
          <a href={item.href} className="underline-offset-4 hover:underline">
            {item.title}
          </a>
        </CardTitle>
        <CardDescription>{item.description}</CardDescription>
        <CardAction>
          <Button
            variant="ghost"
            size="icon-sm"
            type="button"
            aria-pressed={favorite}
            aria-label={
              favorite
                ? `Remove ${item.label} from favorites`
                : `Save ${item.label} to favorites`
            }
            disabled={pending}
            onClick={handleToggle}
          >
            <StarIcon
              data-icon="inline-start"
              fill={favorite ? "currentColor" : "none"}
            />
          </Button>
        </CardAction>
      </CardHeader>
      <CardContent className="flex flex-wrap items-center gap-2">
        {item.badge && <Badge variant="secondary">{item.badge}</Badge>}
        <a
          href={item.href}
          tabIndex={-1}
          aria-hidden="true"
          className="text-sm text-muted-foreground underline-offset-4 hover:text-foreground hover:underline"
        >
          Open preview
        </a>
      </CardContent>
    </Card>
  )
}
