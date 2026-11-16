# ADR 0014: WebDAV en app-wachtwoorden

- Status: geaccepteerd
- Datum: 2026-11-16

## Context

Fase 2 eist dat bestanden ook via een WebDAV-client werken (Finder, Windows Verkenner, rclone,
Cyberduck, later de eigen desktop-app). Zulke clients kunnen geen passkey of TOTP doen.

## Beslissing

### App-wachtwoorden (identity)

1. De server maakt ze aan (26 tekens base32, 130 bits), toont ze één keer en bewaart alleen
   SHA-256. Argon2 is hier niet nodig (het wachtwoord is niet te raden) en zou te traag zijn:
   clients sturen het bij elk verzoek mee.
2. Ze werken alleen voor protocollen (HTTP Basic op `/dav`), nooit om in te loggen op de site;
   het gewone wachtwoord werkt omgekeerd niet voor protocollen (dat vraagt een tweede factor).
3. Aanmaken vraagt een recente login, staat in de audit-log en geeft een beveiligingsmail.
   Fouten tellen mee in dezelfde throttling als inloggen. `last_used_at` wordt hooguit eens per
   minuut bijgewerkt.

### WebDAV (drive)

1. **Library:** `golang.org/x/net/webdav` (BSD, Go-team). Het ondersteunt klasse 1 én 2 (LOCK),
   zonder LOCK koppelen Finder en Verkenner alleen-lezen. Alternatieven: `emersion/go-webdav`
   (geen LOCK) en zelf schrijven (meer werk en randgevallen).
2. **Drie koppelpunten:** `/dav/files/` (eigen bestanden), `/dav/teams/`, `/dav/shared/`.
   Een `FileSystem` koppelt alles aan de drive-service, dus rechten, quota, versies en de
   prullenbak gelden onverkort; DELETE gaat naar de prullenbak.
3. **PUT doet drive zelf:** streamt naar S3 en geeft de juiste status (507 quotum, 413 te
   groot, 409 map ontbreekt, 403 geen recht) in plaats van de 405 van de library. Uploads
   zonder lengte (chunked, Finder) worden ondersteund; S3 krijgt dan delen van 16 MiB, zodat
   het geheugengebruik begrensd blijft.
4. **ETag = versie-ID:** sync-clients zien precies wanneer een bestand veranderde.
5. **Geen XSS via `/dav`:** antwoorden zijn `application/octet-stream` met sandbox-CSP en
   `nosniff`, ook als een browser er een bestand opent.
6. **Locks** staan per gebruiker en koppelpunt in het geheugen van de instantie
   (`webdav.NewMemLS`). Locks zijn adviserend en kort (clients verversen ze); na een herstart
   vragen clients ze opnieuw aan. Dit is de enige staat buiten Postgres/S3/NATS: vóór
   multi-site (meerdere instanties achter één adres) moeten ze naar Postgres.

## Gevolgen

- Getest met eigen integratietests en met `curl` als client; Finder, Verkenner en rclone test
  de gebruiker zelf (er is hier geen GUI-client beschikbaar).
