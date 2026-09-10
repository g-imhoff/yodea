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

export function ListLoading() {
  return (
    <div className="flex flex-col gap-3" aria-label="Loading previews">
      <Skeleton className="h-24 w-full" />
      <Skeleton className="h-24 w-full" />
      <Skeleton className="h-24 w-full" />
    </div>
  )
}

export function ListError({
  message,
  onRetry,
}: {
  message: string
  onRetry: () => void
}) {
  return (
    <div className="flex flex-col gap-3">
      <Alert variant="destructive">
        <TriangleAlertIcon />
        <AlertTitle>Could not load previews</AlertTitle>
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
        <EmptyMedia variant="icon">{icon}</EmptyMedia>
        <EmptyTitle>{title}</EmptyTitle>
        <EmptyDescription>{description}</EmptyDescription>
      </EmptyHeader>
    </Empty>
  )
}
