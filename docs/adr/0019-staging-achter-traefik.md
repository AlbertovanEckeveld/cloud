# ADR 0019: Staging achter Traefik

- Status: geaccepteerd (vervangt "geen reverse proxy; het platform doet zelf HTTPS")
- Datum: 2026-11-17

## Context

Het platform moet testbaar zijn op `ave-dev-cloud.albertove.cloud` met HTTPS (passkeys
werken alleen met HTTPS) en Collabora via hetzelfde adres. Het domein is alleen via NetBird
bereikbaar, dus Let's Encrypt kan het alleen via DNS-01 controleren.

## Beslissing

1. **Traefik als reverse proxy** voor TLS, omdat het DNS-01 voor de meeste DNS-providers
   ingebouwd heeft (Caddy vraagt een eigen build met plug-in). Routes komen uit een bestand:
   Traefik krijgt **geen toegang tot de Docker-socket** (dat staat gelijk aan root). Time-outs
   12 uur, zodat uploads tot 10 GB werken. TLS 1.2+, SNI strikt.
2. **Collabora op hetzelfde adres** via Traefik (`/browser`, `/hosting`, `/cool`); zijn
   beheerconsole staat uit en is in Traefik geblokkeerd. Collabora zit niet op het netwerk met
   de database. Platform en Collabora bereiken elkaar via Traefik onder de publieke naam
   (netwerk-alias), met het echte certificaat.
3. **Echte client-adressen:** `TRUSTED_PROXIES` (vast adres van Traefik). Alleen van daaruit
   telt `X-Forwarded-For`, en alleen het meest rechtse adres dat geen proxy is. Zonder dit zou
   de rate limiting per IP voor iedereen samen gelden en de audit-log alleen Traefik tonen.
4. **Productiemodus** blijft streng; de enige uitzondering is `PRIVATE_NETWORK_HOSTS`:
   containernamen (zonder punt, geen adressen) die zonder TLS mogen, omdat ze op het privé
   Docker-netwerk staan (Postgres en RustFS).
5. **Eigen database, rol en bucket** voor staging; de rol is eigenaar van zijn database, geen
   superuser. De platform-container is alleen-lezen, zonder capabilities, `no-new-privileges`.
6. Alleen Traefik publiceert poorten, en alleen op het NetBird-adres.

## Gevolgen

- Postgres, pgAdmin en RustFS publiceren in `docker/dev-setup` nog poorten op alle interfaces;
  voor iets anders dan ontwikkelen moeten die dicht of op het NetBird-adres.
- Het image en de compose zijn niet door de assistent gedraaid (geen Docker-toegang): de eerste
  start is ook een test.
