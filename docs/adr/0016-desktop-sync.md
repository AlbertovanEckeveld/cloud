# ADR 0016: Desktop-synchronisatie

- Status: geaccepteerd
- Datum: 2026-11-16

## Context

`drive` is pas af met een desktop-app die foutloos synchroniseert (CLAUDE.md). Gekozen: Go +
Wails, eerst Linux en macOS, volledige kopie van gekozen mappen, inloggen via de browser.
Dit ADR beschrijft de serverkant (2.6a) en de sync-engine (2.6b); de app volgt in 2.6c.

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

### Sync-engine (module `desktop/`)

1. **Drieweg-vergelijking per pad:** schijf, server en *base* (hoe het pad eruitzag na de laatste
   geslaagde sync, in SQLite). Alleen hier gewijzigd: uploaden; alleen daar: downloaden; aan
   beide kanten: conflict, waarbij beide versies blijven ("naam (conflict apparaat datum)").
   Hier verwijderd maar daar gewijzigd: de gewijzigde versie wint, dus er gaat nooit iets
   verloren. Een map blijft bestaan zolang er iets in zit dat bewaard moet worden.
2. **Geen beslissingen op oude informatie:** lukt het bijwerken vanaf de server niet (offline),
   dan doet een ronde niets. Wat de client zelf aanmaakt, gaat meteen in de lokale spiegel.
3. **De server is onbetrouwbare invoer:** elke naam wordt gecontroleerd (geen `/`, `\`, `..`,
   besturingstekens), elementen met een onveilig of onbekend pad worden genegeerd, en alle
   schijfacties gaan via `os.Root`: de kernel weigert elke ontsnapping uit de sync-map, ook via
   symlinks. Bestanden worden met `O_NOFOLLOW` geopend (geen omwisselen voor een link tussen
   scannen en openen).
4. **Integriteit:** downloads gaan naar een tijdelijk bestand, de SHA-256 moet overeenkomen met
   die van de server, dan pas een atomaire rename. Wat sinds de scan op schijf veranderde, wordt
   niet overschreven of verwijderd.
5. **Massaverwijder-rem:** een ronde die meer dan 20 items én meer dan 30% van alles zou
   verwijderen, stopt en vraagt bevestiging (ransomware, verkeerd gekoppelde map, vergissing).
   Verwijderen op de server gaat naar de prullenbak; oude versies blijven bewaard.
6. Nooit gesynchroniseerd: symlinks, apparaten, sockets, OS-rommel (`.DS_Store`, `Thumbs.db`,
   `._*`), lock-bestanden van Office/LibreOffice en de eigen tijdelijke bestanden.
7. macOS-namen (NFD) worden als NFC vergeleken, zoals de server ze opslaat.
8. De status-database is alleen leesbaar voor de eigenaar (0600). Tokens zitten niet in de engine.
9. Hernoemen wordt (nog) als verwijderen + nieuw gesynchroniseerd: veilig, maar kost bandbreedte
   en de versiegeschiedenis staat dan bij het oude item in de prullenbak.

## Gevolgen

- `gone` voor een gedeelde root kan ID's van andere nodes van de eigenaar bevatten (zonder naam
  of inhoud); de client negeert ID's die hij niet kent.
- Transactie-ID's zijn per database: bij multi-site-replicatie moet de cursor per site worden
  (of een logische replicatiepositie); dat hoort bij de replicatiefase.
