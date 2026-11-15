# Cloud

Een zelfgebouwd private-cloudplatform: één Go-binary met een ingebedde SvelteKit-frontend,
PostgreSQL voor metadata en S3 voor bestanden. Achtergrond, roadmap en werkafspraken staan in
[CLAUDE.md](CLAUDE.md); architectuurbeslissingen in [docs/adr/](docs/adr/).

**Status:** fase 0.1 — skelet. Het platform start, migreert de database, controleert Postgres
en S3, en toont een statuspagina met alle actieve sites.

## Vereisten

- Go 1.27+
- Node.js 26+ met npm
- Een PostgreSQL 16+ database en een S3-compatibele bucket (zie [docker/dev-setup/](docker/dev-setup/))
- Docker (alleen voor `make docker`)

## Snel starten

```sh
cp .env.example .env    # vul database- en S3-gegevens in
make run                # bouwt frontend + binary en start op http://127.0.0.1:8080
```

Tijdens het ontwikkelen: `make dev` start de backend op `:8080` en Vite met hot reload op
<http://localhost:5173>; Vite stuurt `/api` door naar de backend.

De Go-code bedt `web/build` in. Bouw daarom eerst de frontend (`make web`) voordat je
`go build`, `go test` of `go vet` los draait; de make-targets doen dat automatisch.

## Make-targets

| Target | Doet |
|---|---|
| `make check` | `gofmt`, `go vet`, `svelte-check` |
| `make test` | Unit tests: Go (met race detector) en frontend (Vitest) |
| `make test-integration` | Alle tests, plus integratietests tegen de Postgres en S3 uit `.env` |
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
| `/api/v1/status` | Status van deze instantie en de afhankelijkheden (JSON). |
| `/api/v1/sites` | Alle sites met versie en laatste heartbeat (JSON). |
| overige paden | De frontend (single-page app). |

> De API heeft nog geen authenticatie; die komt met `identity` (fase 1). Stel het platform
> tot die tijd niet open buiten je eigen netwerk.

## Container

```sh
make docker
docker run --rm --env-file .env -e HTTP_ADDR=:8080 -p 127.0.0.1:8080:8080 platform:<versie>
```

Het image is distroless (geen shell), draait als non-root en heeft een ingebouwde
health check (`/platform healthcheck`, die `/readyz` aanroept).

## Structuur

```
cmd/platform/        main: configuratie, opstarten, routes
internal/core/       gedeelde basis: config, logging, database, storage, health, httpserver, site
migrations/<module>/ SQL-migraties per module; de mapnaam is ook het Postgres-schema
web/                 SvelteKit-frontend; web/embed.go bedt web/build in de binary in
docker/dev-setup/    Postgres, pgAdmin en RustFS (S3) voor de ontwikkelserver
docs/adr/            architectuurbeslissingen
```
