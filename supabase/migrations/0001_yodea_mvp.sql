-- yodea MVP metadata schema: deployed sites, per-viewer visit history,
-- and per-viewer favorites. The local file store (internal/store/store.go)
-- mirrors this row model one to one; set YODEA_STORE=supabase to run
-- against these tables via the PostgREST data access in
-- internal/store/supabase.go.
--
-- Personal-rows-only rules: sites, site_views, and favorites are visible
-- and writable only by their owning viewer, EXCEPT that any logged-in user
-- (authenticated role) may SELECT sites rows so previews serve team-wide
-- and any viewer can favorite a preview they can open.

create table if not exists public.sites (
  label      text primary key,
  user_id    text not null,
  project    text not null,
  files      integer not null default 0,
  bytes      bigint not null default 0,
  updated_at timestamptz not null default now()
);
create index if not exists sites_user_id_idx on public.sites (user_id);

create table if not exists public.site_views (
  id      bigint generated always as identity primary key,
  user_id text not null,
  label   text not null,
  at      timestamptz not null default now()
);
create index if not exists site_views_user_id_at_idx on public.site_views (user_id, at desc);

create table if not exists public.favorites (
  user_id    text not null,
  label      text not null,
  created_at timestamptz not null default now(),
  primary key (user_id, label)
);
create index if not exists favorites_user_id_idx on public.favorites (user_id);

alter table public.sites enable row level security;
alter table public.site_views enable row level security;
alter table public.favorites enable row level security;

-- Owners manage their own rows.
drop policy if exists "sites_owner_all" on public.sites;
create policy "sites_owner_all" on public.sites
  for all to authenticated using (auth.uid()::text = user_id)
  with check (auth.uid()::text = user_id);

-- Any logged-in user may read any preview row (preview serving plus
-- favoriting); writes stay owner-only via the policy above.
drop policy if exists "sites_preview_read" on public.sites;
create policy "sites_preview_read" on public.sites
  for select to authenticated using (true);

drop policy if exists "site_views_owner_all" on public.site_views;
create policy "site_views_owner_all" on public.site_views
  for all to authenticated using (auth.uid()::text = user_id)
  with check (auth.uid()::text = user_id);

drop policy if exists "favorites_owner_all" on public.favorites;
create policy "favorites_owner_all" on public.favorites
  for all to authenticated using (auth.uid()::text = user_id)
  with check (auth.uid()::text = user_id);

-- Orphan cleanup: favorite rows whose preview is gone are filtered at read
-- time (RLS stops owners deleting each other's rows); this helper lets an
-- operator or scheduled job purge them with the service key.
-- delete from public.favorites f where not exists
--   (select 1 from public.sites s where s.label = f.label);

-- Grants: RLS policies alone grant nothing without table/sequence
-- privileges. The authenticated role needs DML on all three tables so its
-- owner plus preview-read policies are usable live, plus sequence usage for
-- the site_views identity column on inserts.
grant select, insert, update, delete on public.sites to authenticated;
grant select, insert, update, delete on public.site_views to authenticated;
grant select, insert, update, delete on public.favorites to authenticated;
grant usage, select on sequence public.site_views_id_seq to authenticated;
