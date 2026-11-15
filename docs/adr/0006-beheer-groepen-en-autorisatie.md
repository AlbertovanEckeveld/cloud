# ADR 0006: Beheer, groepen en autorisatie

- Status: geaccepteerd (deelmodel: voorgesteld, wordt gebouwd in fase 2)
- Datum: 2026-11-15

## Context

Fase 1 moet beheerders gebruikers laten uitnodigen en groepen laten beheren. CLAUDE.md
vraagt één centrale autorisatiecheck in `identity` voor gebruikers, groepen en per-item delen.

## Beslissing

### Rollen

Twee rollen: **gebruiker** en **beheerder**. De API dwingt dit af (`RequireAdmin`); de UI
verbergt alleen wat je niet mag.

- **Beheerders beheren accounts, geen inhoud.** Een beheerder krijgt geen automatische
  toegang tot bestanden, foto's, agenda of kluis van anderen. Wil een beheerder iets van een
  ander zien, dan moet dat gedeeld worden zoals voor iedereen.
- **Beheerwijzigingen vereisen recente authenticatie** (15 minuten, zie ADR 0005).
- **Geen lockout:** je kunt jezelf niet uitschakelen, degraderen of resetten, en de laatste
  actieve beheerder kan niet verdwijnen. Wijzigingen aan beheerders nemen een rij-lock op
  alle actieve beheerders, zodat twee beheerders elkaar niet tegelijk kunnen degraderen.
- **Uitschakelen** beëindigt direct alle sessies en lopende ceremonies en maakt open
  activatielinks ongeldig. Inloggen geeft daarna dezelfde algemene fout als een fout wachtwoord.
- **Gebruikersnamen zijn onveranderlijk;** ze worden de stabiele identiteit in OIDC
  (`preferred_username`). De weergavenaam is wel aan te passen.

### Groepen

Groepen hebben een technische naam (zelfde regels als gebruikersnamen) en een vrije
omschrijving. De naam gaat in fase 1.4 als `groups`-claim naar OIDC-clients, die er rollen
aan koppelen. Hernoemen kan, met een waarschuwing in de UI.

### Audit-log

Elke beheeractie wordt vastgelegd met *door* (actor) en *betreft* (subject). Beheerders zien
alle gebeurtenissen, gepagineerd op UUIDv7 (tijdsgeordend), zodat geen offset-paginering
nodig is.

### Deelmodel (ontwerp, gebouwd in fase 2)

Het deelmodel wordt pas gebouwd zodra `drive` het nodig heeft: code zonder gebruiker wordt
niet getoetst en hoort niet in de repo. Het voorgestelde ontwerp:

- Tabel `identity.grants (resource_type, resource_id, subject_type, subject_id, permission)`,
  met `subject_type` = gebruiker of groep en `permission` = `view` < `edit` < `manage`.
- De module die een item maakt, geeft de maker `manage`. Alleen `manage` mag verder delen.
- Eén Go-interface in `identity` voor alle modules: `Check(ctx, user, resource, permission)`,
  `Grant`, `Revoke`, `Grants` en `Accessible(ctx, user, resourceType, permission)` voor lijsten.
- Groepsrechten worden bij elke check opgelost via `group_members`, zodat uit een groep halen
  direct effect heeft.
- Openbare deellinks (fase 2): eigen token (alleen de hash opgeslagen), verloopdatum,
  optioneel wachtwoord, alleen `view`.

## Gevolgen

- Fase 2 begint met het bouwen en testen van het deelmodel, samen met de eerste gebruiker ervan.
