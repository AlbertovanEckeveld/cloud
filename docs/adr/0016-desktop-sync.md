# ADR 0016: Desktop-synchronisatie (deel 1: server)

- Status: geaccepteerd
- Datum: 2026-11-16

## Context

`drive` is pas af met een desktop-app die foutloos synchroniseert (CLAUDE.md). Gekozen: Go +
Wails, eerst Linux en macOS, volledige kopie van gekozen mappen, inloggen via de browser.
Dit ADR beschrijft de serverkant (2.6a); de sync-engine en app volgen.

## Beslissing

### Apparaat koppelen

1. De app is de ingebouwde publieke OIDC-client `cloud-desktop`: authorization code + PKCE via
   de systeembrowser, redirect naar `http://127.0.0.1:<vrije poort>/callback` (RFC 8252 §7.3).
   Je wachtwoord of passkey komt nooit in de app; de app krijgt een access token en een
   roterend refresh token (30 dagen).
2. De drive-API (`RequireUserOrDevice`) accepteert naast de sessiecookie alleen access tokens
   van deze client. Zo'n apparaat mag nooit beheren (`RequireAdmin` weigert apparaten) en nooit
   iets dat een recente login vraagt. Elke aanroep controleert het token in de database, dus
   intrekken ("Apps" op de accountpagina) of uitschakelen werkt meteen.

### Wijzigingen volgen

1. Een databasetrigger op `drive.nodes` schrijft elke wijziging in `drive.changes` (met de
   eigenaar van de boom). Geen codepad kan een wijziging vergeten.
2. **Cursor = (transactie-ID, wijzigings-ID).** De server geeft alleen wijzigingen van
   transacties onder `pg_snapshot_xmin` uit: die zijn gegarandeerd klaar. Met een tijd-cursor
   kan een transactie die eerder begon maar later commit, gemist worden; zo niet.
3. **Wijzigingen zijn toestand, geen gebeurtenissen:** per node de huidige stand, of `gone`
   (verwijderd, prullenbak, uit de map verplaatst, niet meer gedeeld). Dubbel ontvangen is
   onschuldig; de client hoeft geen volgorde van gebeurtenissen na te spelen.
4. **Roots:** eigen bestanden, teammappen en gedeelde mappen. Een volledige boom (`/sync/tree`)
   wordt in één consistente momentopname gelezen, samen met de begincursor.
5. Elk bestand komt met versie-ID en SHA-256, zodat de client downloads kan controleren.
6. Wijzigingen worden 30 dagen bewaard; een oudere cursor geeft 410 en de client leest de boom
   opnieuw in.

## Gevolgen

- `gone` voor een gedeelde root kan ID's van andere nodes van de eigenaar bevatten (zonder naam
  of inhoud); de client negeert ID's die hij niet kent.
- Transactie-ID's zijn per database: bij multi-site-replicatie moet de cursor per site worden
  (of een logische replicatiepositie); dat hoort bij de replicatiefase.
