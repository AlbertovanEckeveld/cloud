# ADR 0009: Drive – opslag van bestanden

- Status: geaccepteerd
- Datum: 2026-11-16

## Context

`drive` vervangt de bestandsfunctie van Nextcloud. Bestanden kunnen groot zijn (tot 10 GB),
komen van onbetrouwbare bronnen en moeten later via WebDAV, deellinks en Collabora bereikbaar
zijn. Het platform moet multi-site blijven (ADR 0001).

## Beslissing

1. **Inhoud in S3, structuur in Postgres.** `drive.nodes` bevat de boom (mappen, bestanden,
   namen), `drive.versions` de versies. Elke versie is één S3-object onder een onveranderlijke
   sleutel `drive/<versie-UUID>`: nieuwe inhoud krijgt een nieuwe sleutel, zodat replicatie
   tussen sites nooit hoeft te kiezen tussen twee varianten van één object.
2. **Streaming.** Uploads gaan rechtstreeks naar S3 (multipart voor grote bestanden), terwijl
   SHA-256 en het aantal bytes worden bijgehouden; het aantal moet exact gelijk zijn aan de
   aangekondigde `Content-Length`, anders wordt de upload geweigerd. Downloads streamen uit S3
   met ondersteuning voor Range-requests (hervatten). Upload- en downloadrequests krijgen een
   eigen deadline van 12 uur; de algemene servertimeouts (30 s) blijven voor de rest gelden.
3. **Geen XSS via geüploade bestanden.** Downloads zijn altijd `application/octet-stream` met
   `Content-Disposition: attachment` en `Content-Security-Policy: default-src 'none'; sandbox`.
   De bestandsnaam in de header wordt gecodeerd (RFC 6266/2231). Voorbeeldweergave van veilige
   typen volgt in 2.3, met dezelfde voorzichtigheid.
4. **Namen** worden genormaliseerd naar Unicode NFC (macOS gebruikt NFD), mogen geen `/`, `\`
   of besturingstekens bevatten, en zijn per map uniek ongeacht hoofdletters (Windows en macOS
   zijn hoofdletterongevoelig).
5. **Autorisatie op één plek:** elke handeling loopt via `authorize()` in drive. In 2.1 alleen
   de eigenaar; 2.2 breidt dit uit met het deelmodel uit ADR 0006. Bestanden van een ander geven
   404, niet 403, zodat hun bestaan niet uitlekt. Drive kent identity alleen via een klein
   `Auth`-contract (middleware + "wie is de gebruiker").
6. **Prullenbak:** verwijderen zet `trashed_at` op het gekozen item; alles eronder verdwijnt mee
   uit beeld. Na 30 dagen wordt het automatisch definitief verwijderd. Terugzetten zet het item
   terug op zijn plek, of op het hoogste niveau als de oude map weg is, met "(hersteld)" bij een
   naamconflict.
7. **S3-objecten worden pas verwijderd ná de database.** Een wachtrij (`drive.object_deletions`)
   wordt in dezelfde transactie gevuld en met `FOR UPDATE SKIP LOCKED` en backoff afgewerkt.
   Ook mislukte of afgebroken uploads komen erin, zodat er geen verweesde objecten achterblijven.
8. **Quota:** standaard geen limiet; een beheerder kan per gebruiker een limiet zetten. Alle
   versies tellen mee, ook in de prullenbak. De controle gebeurt vóór het uploaden (geen
   gigabytes voor niets) en opnieuw in de transactie (gelijktijdige uploads).

## Gevolgen

- Een bestand vervangen maakt een nieuwe versie; versiebeheer in de UI volgt in 2.2.
- Een object dat na een crash tussen upload en database-commit achterblijft, wordt nog niet
  automatisch gevonden; een periodieke vergelijking S3 ↔ database is een latere taak.
