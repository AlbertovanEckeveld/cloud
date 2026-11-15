# Cloud

Een zelfgebouwd private-cloudplatform: één Go-binary met een ingebedde SvelteKit-frontend,
PostgreSQL voor metadata en S3 voor bestanden. Achtergrond, roadmap en werkafspraken staan in
[CLAUDE.md](CLAUDE.md); architectuurbeslissingen in [docs/adr/](docs/adr/).

**Status:** fase 2.1 — `drive`: mappen en bestanden (uploaden, downloaden, verplaatsen,
prullenbak, quota). Fase 1 — `identity` (vervangt Authentik) is af. Inloggen met passkey, of met wachtwoord
plus authenticator-app (TOTP) of herstelcode; sessiebeheer; een beheeromgeving voor
gebruikers, groepen, apps, audit-log en systeemstatus; een OpenID Connect-provider zodat
andere apps met je Cloud-account inloggen; optioneel mail voor activatielinks en
beveiligingsmeldingen.

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
`http://localhost:8080`. Browsers staan passkeys alleen toe via HTTPS of op `localhost`; op een
andere hostnaam via http (zoals nu via NetBird) log je in met wachtwoord + authenticator-app.
Verdere gebruikers nodig je uit via **Beheer → Gebruikers**. Kwijtgeraakt?
`platform user reset -username jouwnaam` (of **Account resetten** in Beheer) maakt het account
leeg en geeft een nieuwe link. Draait het platform in Docker, gebruik dan `docker exec <container> /platform user …`.

Tijdens het ontwikkelen: `make dev` start de backend op `:8080` en Vite met hot reload op
<http://localhost:5173>; Vite stuurt `/api` door naar de backend.

De Go-code bedt `web/build` in. Bouw daarom eerst de frontend (`make web`) voordat je
`go build`, `go test` of `go vet` los draait; de make-targets doen dat automatisch.

## Apps laten inloggen (OpenID Connect)

Registreer de app in **Beheer → Apps** en vul bij de app in: de issuer
(`<PUBLIC_URL>/oidc`), het client-ID en het client-secret. Voorbeeld voor Grafana
(`grafana.ini`):

```ini
[auth.generic_oauth]
enabled = true
name = Cloud
client_id = <client-id>
client_secret = <client-secret>
scopes = openid profile email groups
auth_url = <PUBLIC_URL>/oidc/authorize
token_url = <PUBLIC_URL>/oidc/oauth/token
api_url = <PUBLIC_URL>/oidc/userinfo
use_pkce = true
login_attribute_path = preferred_username
role_attribute_path = contains(groups[*], 'beheer') && 'Admin' || 'Viewer'
```

De redirect-URI van Grafana is `<grafana-url>/login/generic_oauth`. Beperk desgewenst de
toegang tot een groep.

Beschikbare claims: `sub`, `preferred_username`, `name`, `given_name`, `family_name`, `locale`,
`picture` (scope `profile`), `email`, `email_verified` (scope `email`), `groups` (scope
`groups`), en `amr`/`acr` (hoe en hoe sterk er is ingelogd; vraag met `acr_values=phr` om een
passkey-login). Apps met een back-channel logout-URI horen het als een gebruiker overal uitlogt
of wordt uitgeschakeld. Zie [ADR 0008](docs/adr/0008-oidc-provider.md).

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
| `/api/v1/admin/*` | Gebruikers, groepen, apps en audit-log beheren. Alleen beheerders. |
| `/api/v1/drive/*` | Bestanden en mappen: lijst, upload, download, hernoemen, verplaatsen, prullenbak, verbruik. |
| `/api/v1/oidc/*` | Inlogverzoeken van apps afhandelen (voor de pagina `/connect`). |
| `/oidc/*` | De OpenID Connect-provider: discovery, authorize, token, userinfo, keys. |
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
                     throttling, audit-log, gebruikers- en groepsbeheer, OIDC-provider
internal/drive/      bestanden en mappen: boom in Postgres, inhoud in S3, prullenbak, quota
migrations/<module>/ SQL-migraties per module; de mapnaam is ook het Postgres-schema
web/                 SvelteKit-frontend; web/embed.go bedt web/build in de binary in
docker/dev-setup/    Postgres, pgAdmin en RustFS (S3) voor de ontwikkelserver
docs/adr/            architectuurbeslissingen
```
