import { RotateCcwIcon, TriangleAlertIcon } from "lucide-react"

import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert"
import { Button } from "@/components/ui/button"
import {
  Empty,
  EmptyDescription,
  EmptyHeader,
  EmptyMedia,
  EmptyTitle,
} from "@/components/ui/empty"
import { Skeleton } from "@/components/ui/skeleton"

export function ListLoading({ label = "Loading previews" }: { label?: string }) {
  return (
    <div className="flex flex-col gap-3" role="status" aria-label={label}>
      <Skeleton className="h-24 w-full" aria-hidden="true" />
      <Skeleton className="h-24 w-full" aria-hidden="true" />
      <Skeleton className="h-24 w-full" aria-hidden="true" />
    </div>
  )
}

export function ListError({
  message,
  onRetry,
  title = "Could not load previews",
}: {
  message: string
  onRetry: () => void
  title?: string
}) {
  return (
    <div className="flex flex-col gap-3">
      <Alert variant="destructive">
        <TriangleAlertIcon />
        <AlertTitle>{title}</AlertTitle>
        <AlertDescription>{message}</AlertDescription>
      </Alert>
      <div>
        <Button variant="outline" type="button" onClick={onRetry}>
          <RotateCcwIcon data-icon="inline-start" />
          Retry
        </Button>
      </div>
    </div>
  )
}

export function ListEmpty({
  icon,
  title,
  description,
}: {
  icon: React.ReactNode
  title: string
  description: string
}) {
  return (
    <Empty>
      <EmptyHeader>
        <EmptyMedia variant="icon" aria-hidden="true">{icon}</EmptyMedia>
        <EmptyTitle>{title}</EmptyTitle>
        <EmptyDescription>{description}</EmptyDescription>
      </EmptyHeader>
    </Empty>
  )
}
