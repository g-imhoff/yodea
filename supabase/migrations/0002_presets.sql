create table if not exists public.presets (
  user_id    text not null,
  label      text not null,
  name       text not null,
  data       jsonb not null,
  updated_at timestamptz not null default now(),
  primary key (user_id, label, name)
);

alter table public.presets enable row level security;

drop policy if exists "presets_owner_all" on public.presets;
create policy "presets_owner_all" on public.presets
  for all to authenticated using (auth.uid()::text = user_id)
  with check (auth.uid()::text = user_id);

grant select, insert, update, delete on public.presets to authenticated;
