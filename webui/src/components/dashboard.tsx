import { useCallback, useState } from "react"

import {
  HistoryIcon,
  InboxIcon,
  LayoutGridIcon,
  LogOutIcon,
  StarIcon,
} from "lucide-react"

import {
  AuthError,
  formatDateTime,
  previewUrl,
  type Favorite,
  type Site,
  type View,
} from "@/lib/api"
import type { ToggleFavoriteResult } from "@/lib/favorites"
import { ThemeToggle } from "@/components/theme-toggle"
import { PreviewCard, type PreviewItem } from "@/components/preview-card"
import { ListEmpty, ListError, ListLoading } from "@/components/view-state"
import { Button } from "@/components/ui/button"
import { Separator } from "@/components/ui/separator"
import { Spinner } from "@/components/ui/spinner"
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs"

export type LoadStatus = "loading" | "error" | "ready"

export interface DashboardData {
  sites: Site[]
  sitesStatus: LoadStatus
  views: View[]
  viewsStatus: LoadStatus
  favorites: Favorite[]
  favStatus: LoadStatus
  onToggleFavorite: (
    label: string
  ) => void | Promise<void | ToggleFavoriteResult>
  onRetrySites: () => void
  onRetryViews: () => void
  onRetryFavorites: () => void
  onLogout: () => void
  loggingOut: boolean
  initialView?: string
}

function siteItem(site: Site): PreviewItem {
  return {
    label: site.label,
    title: site.project,
    description: `push with: yodea push --project ${site.project}`,
    href: previewUrl(site.label),
    badge: `${site.files} ${site.files === 1 ? "file" : "files"}`,
  }
}

export function Dashboard(props: DashboardData) {
  const {
    sites,
    sitesStatus,
    views,
    viewsStatus,
    favorites,
    favStatus,
    onToggleFavorite,
    onRetrySites,
    onRetryViews,
    onRetryFavorites,
    onLogout,
    loggingOut,
    initialView = "previews",
  } = props

  const projectsByLabel = new Map(sites.map((s) => [s.label, s.project]))
  const favoriteLabels = new Set(favorites.map((f) => f.label))
  // Favorite hrefs use the protocol-aware client preview URL so plain-http
  // dev stays reachable; fav.link is server-built https metadata and is
  // intentionally unused here.

  // Last failed star toggle (per-label). Cleared on the next attempt so a
  // flaky network shows a visible error with retry instead of leaving the
  // star silently unfilled. AuthError stays silent here; favorites.ts already
  // handed control to the login phase.
  const [favToggleError, setFavToggleError] = useState<{ label: string } | null>(
    null
  )

  const handleToggleFavorite = useCallback(
    async (label: string) => {
      setFavToggleError(null)
      try {
        const result = await onToggleFavorite(label)
        if (result && !result.ok) {
          if (result.error instanceof AuthError) return
          setFavToggleError({ label })
        }
      } catch (err) {
        if (err instanceof AuthError) return
        setFavToggleError({ label })
      }
    },
    [onToggleFavorite]
  )

  const handleRetryToggle = useCallback(() => {
    if (!favToggleError) return
    void handleToggleFavorite(favToggleError.label)
  }, [favToggleError, handleToggleFavorite])
  // Deleted previews are already dropped server-side, so this list is
  // rendered as returned.
  const favoriteItems: PreviewItem[] = favorites.map((fav) => ({
    label: fav.label,
    title: fav.project,
    description: `by ${fav.owner}`,
    href: previewUrl(fav.label),
  }))

  function renderCards(items: PreviewItem[]) {
    return (
      <div className="flex flex-col gap-3">
        {items.map((item) => (
          <PreviewCard
            key={item.label}
            item={item}
            favorite={favoriteLabels.has(item.label)}
            onToggleFavorite={handleToggleFavorite}
          />
        ))}
      </div>
    )
  }

  return (
    <div className="mx-auto flex w-full max-w-3xl flex-col gap-4 p-4">
      <header className="flex items-center gap-2">
        <h1 className="text-lg font-medium">yodea</h1>
        <div className="flex-1" />
        <ThemeToggle />
        <Button
          variant="outline"
          type="button"
          disabled={loggingOut}
          onClick={onLogout}
        >
          {loggingOut ? (
            <Spinner data-icon="inline-start" />
          ) : (
            <LogOutIcon data-icon="inline-start" />
          )}
          {loggingOut ? "Logging out" : "Log out"}
        </Button>
      </header>
      <Separator />
      {favToggleError && (
        <ListError
          title="Could not save favorite"
          message={`Could not save ${favToggleError.label} to favorites. Check your connection and try again.`}
          onRetry={handleRetryToggle}
        />
      )}
      <Tabs defaultValue={initialView}>
        <TabsList>
          <TabsTrigger value="previews">
            <LayoutGridIcon data-icon="inline-start" />
            My previews
          </TabsTrigger>
          <TabsTrigger value="viewed">
            <HistoryIcon data-icon="inline-start" />
            Recently viewed
          </TabsTrigger>
          <TabsTrigger value="favorites">
            <StarIcon data-icon="inline-start" />
            Favorites
          </TabsTrigger>
        </TabsList>
        <TabsContent value="previews">
          {sitesStatus === "loading" && <ListLoading label="Loading my previews" />}
          {sitesStatus === "error" && (
            <ListError
              message="My previews are unavailable right now."
              onRetry={onRetrySites}
            />
          )}
          {sitesStatus === "ready" &&
            (sites.length === 0 ? (
              <ListEmpty
                icon={<InboxIcon />}
                title="Nothing pushed yet"
                description="Run yodea init, then yodea push."
              />
            ) : (
              renderCards(sites.map(siteItem))
            ))}
        </TabsContent>
        <TabsContent value="viewed">
          {viewsStatus === "loading" && (
            <ListLoading label="Loading recently viewed" />
          )}
          {viewsStatus === "error" && (
            <ListError
              message="Recently viewed previews are unavailable right now."
              onRetry={onRetryViews}
            />
          )}
          {viewsStatus === "ready" &&
            (views.length === 0 ? (
              <ListEmpty
                icon={<HistoryIcon />}
                title="Nothing viewed yet"
                description="Previews you open will show up here."
              />
            ) : (
              renderCards(
                views.map((view) => ({
                  label: view.label,
                  title: projectsByLabel.get(view.label) ?? view.label,
                  description: `Viewed ${formatDateTime(view.at)}`,
                  href: previewUrl(view.label),
                }))
              )
            ))}
        </TabsContent>
        <TabsContent value="favorites">
          {favStatus === "loading" && <ListLoading label="Loading favorites" />}
          {favStatus === "error" && (
            <ListError
              message="Favorites are unavailable right now."
              onRetry={onRetryFavorites}
            />
          )}
          {favStatus === "ready" &&
            (favoriteItems.length === 0 ? (
              <ListEmpty
                icon={<StarIcon />}
                title="No favorites yet"
                description="Flag a preview with the star to find it back here."
              />
            ) : (
              renderCards(favoriteItems)
            ))}
        </TabsContent>
      </Tabs>
    </div>
  )
}
