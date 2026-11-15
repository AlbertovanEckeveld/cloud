# Cloud

Een zelfgebouwd private-cloudplatform: één Go-binary met een ingebedde SvelteKit-frontend,
PostgreSQL voor metadata en S3 voor bestanden. Achtergrond, roadmap en werkafspraken staan in
[CLAUDE.md](CLAUDE.md); architectuurbeslissingen in [docs/adr/](docs/adr/).

**Status:** fase 1.3 — accounts, inloggen en beheer. Inloggen met passkey, of met wachtwoord
plus authenticator-app (TOTP) of herstelcode; sessiebeheer; een beheeromgeving voor
gebruikers, groepen, audit-log en systeemstatus. Optioneel uitgaande mail voor
activatielinks en beveiligingsmeldingen.

## Vereisten

- Go 1.27+
- Node.js 26+ met npm
- Een PostgreSQL 16+ database en een S3-compatibele bucket (zie [docker/dev-setup/](docker/dev-setup/))
- Docker (alleen voor `make docker`)

## Snel starten

```sh
cp .env.example .env    # vul database- en S3-gegevens in, en een sleutel:
                        #   ENCRYPTION_KEY=$(openssl rand -base64 32)
make run                # bouwt frontend + binary en start op http://127.0.0.1:8080
```

Maak daarna in een tweede terminal de eerste beheerder aan:

```sh
make build && (set -a; . ./.env; ./bin/platform user create -username jouwnaam -name "Jouw Naam" -admin)
```

Dit drukt een eenmalige activatielink af (72 uur geldig). Open die in de browser, kies een
wachtwoord, koppel een authenticator-app (Aegis, 2FAS, Google Authenticator, …) en bewaar de
herstelcodes. Een passkey voeg je daarna toe op de accountpagina.

Met een SMTP-server in `.env` (`SMTP_*`) worden activatielinks en beveiligingsmeldingen ook
gemaild (zie [ADR 0007](docs/adr/0007-uitgaande-mail.md)).

`PUBLIC_URL` moet een hostnaam zijn (geen IP-adres), anders werken passkeys niet: lokaal
`http://localhost:8080`.
Verdere gebruikers nodig je uit via **Beheer → Gebruikers**. Kwijtgeraakt?
`platform user reset -username jouwnaam` (of **Account resetten** in Beheer) maakt het account
leeg en geeft een nieuwe link. Draait het platform in Docker, gebruik dan `docker exec <container> /platform user …`.

Tijdens het ontwikkelen: `make dev` start de backend op `:8080` en Vite met hot reload op
<http://localhost:5173>; Vite stuurt `/api` door naar de backend.

De Go-code bedt `web/build` in. Bouw daarom eerst de frontend (`make web`) voordat je
`go build`, `go test` of `go vet` los draait; de make-targets doen dat automatisch.

## Make-targets

| Target | Doet |
|---|---|
| `make check` | `gofmt`, `go vet`, `svelte-check` |
| `make test` | Unit tests: Go (met race detector) en frontend (Vitest) |
| `make test-integration` | Alle tests, plus integratietests tegen `TEST_DATABASE_URL` en de S3 uit `.env` |
| `make run` | Bouwt en start het platform met `.env` |
| `make dev` | Backend + Vite-devserver met hot reload |
| `make docker` | Bouwt het container-image `platform:<versie>` |

## Configuratie

Alles via environment variables; zie [.env.example](.env.example) voor de volledige lijst.
Elk geheim kan ook uit een bestand komen (`NAAM_FILE=/run/secrets/...`), bijvoorbeeld via
Docker secrets.

Zonder `APP_ENV` draait het platform in `production`-modus. Dan zijn alleen versleutelde
verbindingen toegestaan (`sslmode=verify-full`, `https://` voor S3) en wordt HSTS gezet.
Bij een ongeldige of onveilige configuratie start het platform niet en meldt het alle
problemen tegelijk.

## Endpoints

| Pad | Doel |
|---|---|
| `/healthz` | Liveness: draait het proces? Controleert bewust geen afhankelijkheden. |
| `/readyz` | Readiness: zijn Postgres en S3 bereikbaar? `503` tijdens afsluiten. |
| `/api/v1/auth/*` | Inloggen (passkey, wachtwoord + TOTP of herstelcode), activeren, uitloggen. |
| `/api/v1/account/*` | Eigen account: wachtwoord, passkeys, herstelcodes, sessies, activiteit. Vereist login. |
| `/api/v1/admin/*` | Gebruikers, groepen en audit-log beheren. Alleen beheerders. |
| `/api/v1/status` | Status van deze instantie en de afhankelijkheden. Alleen beheerders. |
| `/api/v1/sites` | Alle sites met versie en laatste heartbeat. Alleen beheerders. |
| overige paden | De frontend (single-page app). |

## Container

```sh
make docker
docker run --rm --env-file .env -e HTTP_ADDR=:8080 -p 127.0.0.1:8080:8080 platform:<versie>
```

Het image is distroless (geen shell), draait als non-root en heeft een ingebouwde
health check (`/platform healthcheck`, die `/readyz` aanroept).

## Structuur

```
cmd/platform/        main: configuratie, opstarten, routes, CLI (user create/reset)
internal/core/       gedeelde basis: config, logging, database, storage, health, httpserver,
                     sealer (versleuteling at rest), mailer (outbox + SMTP), site
internal/identity/   accounts, wachtwoorden, TOTP, passkeys, herstelcodes, sessies,
                     throttling, audit-log, gebruikers- en groepsbeheer
migrations/<module>/ SQL-migraties per module; de mapnaam is ook het Postgres-schema
web/                 SvelteKit-frontend; web/embed.go bedt web/build in de binary in
docker/dev-setup/    Postgres, pgAdmin en RustFS (S3) voor de ontwikkelserver
docs/adr/            architectuurbeslissingen
```
