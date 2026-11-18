# Projectcontext: eigen private cloud

> Dit bestand geeft een AI-assistent de context van dit project. Houd de secties
> **Huidige status** en **Beslissingen** bij; de rest verandert zelden.

## 1. Wat dit project is

Een zelfgebouwd, geïntegreerd private-cloudplatform dat ik van scratch ontwikkel.
Het vervangt de functionaliteit van bestaande apps, zonder dat die apps zelf draaien:

| Vervangt | Eigen module | Kern |
|---|---|---|
| Authentik | `identity` | Accounts, groepen, passkeys, eigen OIDC-provider, rechten- en deelmodel, audit-log |
| Nextcloud (bestanden) | `drive` | Bestanden op S3, versies, prullenbak, deellinks, quota, WebDAV, WOPI-host |
| Nextcloud groupware / Google Calendar, Tasks, Keep | `pim` | Agenda, contacten, taken, notities (Obsidian-achtige links), CalDAV/CardDAV |
| Immich / Google Foto's | `photos` | Auto-upload, tijdlijn, albums, gezichten, CLIP-zoeken, familiebibliotheek |
| Frigate | `nvr` | Camerastreams, opnames naar S3, motion + objectdetectie, events via MQTT |
| Vaultwarden | `vault` | Zero-knowledge wachtwoordkluis, Bitwarden-compatibele API |
| Gmail | `mail` | SMTP-ontvangst, IMAP, webmail, spamfilter, SPF/DKIM/DMARC |
| ChatGPT / Gemini | `assistant` | Lokale LLM, chat, RAG over eigen notities/documenten/mail |

**Blijft extern (niet zelf bouwen, wel koppelen):**
- **Home Assistant**: login via onze OIDC-provider, NVR-events via MQTT.
- **Collabora Online (zelfgehost)**: documenten bewerken via WOPI; `drive` is de WOPI-host.
- **S3-objectopslag**: ik gebruik een bestaande S3-compatibele server thuis. Geen eigen S3 bouwen.

**Hoofddoel: leren hoe deze systemen werken.** Snelheid is ondergeschikt.

## 2. Hoe je (de AI) met mij samenwerkt

- **Leg uit, niet alleen opleveren.** Bij nieuwe concepten: kort het *waarom*, de
  relevante standaard/RFC, en de alternatieven met afweging. Daarna pas code.
- **Kleine stappen.** Liever één werkend, getest onderdeel dan een grote lap code.
  Stel bij grotere wijzigingen eerst een plan voor en wacht op akkoord.
- **Standaarden eerst.** Implementeer de officiële spec (OIDC, WebAuthn, WebDAV,
  CalDAV, CardDAV, IMAP, SMTP, WOPI, S3, MQTT) zodat bestaande clients werken.
  Verwijs naar de juiste RFC-sectie bij lastige details.
- **Geen nieuwe dependencies zonder te vragen.** Noem de library, waarom, licentie
  en onderhoudsstatus. Voorkeur voor standaardbibliotheek en goed onderhouden libs.
- **Licenties:** neem geen AGPL-code over (bijv. Immich). Lezen als referentie mag.
  MIT/Apache/BSD-code mag als inspiratie, met bronvermelding.
- **Tests bij elke wijziging.** Unit tests, en integratietests tegen echte clients
  waar het om protocollen gaat.
- **Wees kritisch.** Wijs me op fouten, beveiligingsrisico's en slechte ideeën,
  ook als ik er zelf om vraag. Zeg het als je iets niet zeker weet.
- **Taal:** communicatie in het Nederlands; code, identifiers, commits en
  code-comments in het Engels.
- **Ik kom uit de cybersecurity, niet uit app-development.** Leg ontwikkelconcepten
  (Go-idiomen, frontend-build, testopzet) uit; securityconcepten mogen beknopter.
- **Security first** op elk niveau: code, functionaliteit, defaults en deployment.
- **Fase voor fase, nooit vooruitlopen.** Ik test elke fase zelf grondig voordat de
  volgende begint. Elke fase levert een concreet werkende module op die volledig
  geïntegreerd is met wat er al staat (identity, rechten, UI, back-up, monitoring).
- **Backend en frontend groeien samen.** Geen API zonder UI, geen UI zonder API.
- **Alles grondig getest voordat we verder gaan** (zie §2 "Tests bij elke wijziging").
- **Schone repository.** Geen ongebruikte, halve of placeholder-bestanden, geen
  uitgecommentarieerde code, geen "TODO later"-stubs. Wat in de repo staat, wordt gebruikt.

## 3. Architectuur

- **Modulaire monoliet** in Go: één binary, modules als aparte packages met een
  duidelijke publieke interface. Modules praten via Go-interfaces (synchroon) of
  NATS-events (asynchroon). Geen directe toegang tot elkaars tabellen.
- **Workers** als aparte processen die jobs uit NATS JetStream oppakken:
  - `ml-worker` (Python): gezichtsdetectie/-clustering, CLIP-embeddings, objectdetectie.
  - `llm-server`: llama.cpp of Ollama met OpenAI-compatibele API.
  - `media-worker`: ffmpeg (thumbnails, transcodes, opnamesegmenten), go2rtc voor streams.
- **Opslag:** blobs in S3, metadata in PostgreSQL. Nooit bestanden in de database.
- **Netwerk:** alles draait thuis. 

```
Clients (web, telefoon, desktop)
        │ HTTPS
         ──► Thuisserver (Docker Compose)
                              ├─ Go-platform: identity, drive, pim, photos,
                              │               vault, mail, nvr, assistant
                              ├─ Workers: ml (Python), llm, media, backup
                              └─ PostgreSQL+pgvector · S3 · NATS
Extern: Home Assistant (OIDC, MQTT) · Collabora (WOPI)
```

### Multi-site (eis voor de lange termijn)

Het platform gaat uiteindelijk op **twee of meer sites** draaien die elkaar
**hot-hot** (actief-actief) synchroniseren en bij uitval overnemen. Dit wordt nu nog
niet gebouwd, maar elk ontwerp moet het mogelijk houden:

- App-processen zijn **stateless**: geen sessies, caches of bestanden op lokale disk
  of in geheugen die niet uit Postgres/S3/NATS te herstellen zijn.
- **IDs altijd UUIDv7, nooit database-sequences** (geen botsingen tussen sites).
- Elke instantie heeft een **`SITE_ID`**; records en events leggen vast waar ze ontstonden.
- **Events zijn idempotent** en worden via een outbox gepubliceerd (geen verlies, dubbel verwerken is veilig).
- **S3-objecten zijn immutable** (nieuwe versie = nieuwe key), zodat bucket-replicatie triviaal is.
- Per domein expliciet nadenken over **conflictresolutie** bij gelijktijdige writes op twee sites.
- Twee sites alleen kunnen geen veilige automatische failover doen (split-brain);
  daarvoor is een **derde stem (witness)** nodig.

### Frontend

Eenvoudig en rustig van opzet, daardoor gebruiksvriendelijk, maar wel
functionaliteitsrijk. Liever één goed doordacht scherm dan vijf halve.

## 4. Techstack

| Laag | Keuze |
|---|---|
| Backend | Go (laatste stabiele versie), `net/http` + lichte router (chi), `log/slog` |
| Database | PostgreSQL 16+ met pgvector; één schema per module; queries met sqlc of pgx |
| Migraties | goose of golang-migrate, per module |
| Opslag | Bestaande S3-compatibele server thuis (AWS SDK v2 / minio-go client) |
| Events/jobs | NATS + JetStream |
| ML | Python + FastAPI, ONNX Runtime / OpenVINO (Intel iGPU) |
| LLM | llama.cpp of Ollama, OpenAI-compatibele API |
| Frontend | TypeScript + SvelteKit, later PWA |
| Media | ffmpeg, go2rtc |
| Infra | Docker Compose |
| Observability | Prometheus, Grafana, Loki; OpenTelemetry-tracing optioneel |
| Back-up | restic naar offsite S3-bucket + `pg_dump` |
| Protocol-libs (Go) | go-webauthn, go-webdav (incl. CalDAV/CardDAV), go-imap, go-smtp |

## 5. Hardware en hosting (beperkingen)

- Thuisserver: Intel i5-7400 (iGPU bruikbaar via OpenVINO). Een GTX 1050 Ti (4 GB)
  is beschikbaar maar wordt voorlopig niet gebruikt. Reken op kleine LLM's (3–4B)
  en plan zwaar ML-werk 's nachts.
- Bestaande S3-server en Home Assistant-VM (met Frigate) draaien al thuis.

## 6. Repo-structuur

```
/cmd/platform          main.go van de monoliet (opstarten, routes)
/internal/<module>     identity, drive, pim, photos, vault, mail, nvr, assistant
/internal/core         config, logging, database, storage, health, httpserver, site (later: auth, events)
/migrations/<module>   SQL-migraties per module; mapnaam = Postgres-schema
/workers/ml            Python ML-service
/web                   SvelteKit-frontend; web/embed.go bedt web/build in de binary in
/docker/dev-setup      Postgres, pgAdmin, RustFS voor de ontwikkelserver
/deploy                compose voor staging op ave-dev-cloud, update.sh + systemd-timer
/docs/adr              ADR's (architectuurbeslissingen)
```

## 7. Conventies

- **Errors:** wrap met context (`fmt.Errorf("...: %w", err)`); nooit stil negeren.
- **Logging:** gestructureerd met `slog`; nooit wachtwoorden, tokens of kluisdata loggen.
- **Config:** via environment variables; secrets nooit in git.
- **API:** JSON over HTTP, versie in pad (`/api/v1/...`); protocol-endpoints
  (WebDAV, CalDAV, WOPI, Bitwarden-API) volgen hun eigen spec.
- **Rechten:** elke request gaat door één centrale autorisatiecheck in `identity`
  (gebruiker/groep + per-item delen). Nooit per module zelf iets verzinnen.
- **IDs:** UUIDv7. **Tijden:** UTC in de database, `timestamptz`.
- **Beslissingen** vastleggen als korte ADR in `/docs/adr/`.
- **Tests:** unit tests naast de code (`*_test.go`, `*.spec.ts`); integratietests tegen echte
  Postgres/S3 met build-tag `integration` (`make test-integration`). Elke test ruimt zijn data op.
- **Frontend:** UI-teksten in het Nederlands; imports via `#lib/...ts`; geen inline `style=""`
  of `<script>` (de CSP blokkeert die, zie ADR 0003); API-responses runtime valideren.
- **API-fouten:** `{"error": {"code", "message"}}`; nooit interne details (foutstrings,
  hostnamen, SQL) naar de client; die gaan met de request-ID naar het log.

## 8. Beveiligingsregels (niet onderhandelbaar)

- Geen eigen crypto-primitieven; alleen bewezen libraries (`crypto/*`, x/crypto).
- `vault`: zero-knowledge. De server ziet alleen versleutelde data en kan de kluis
  nooit ontsleutelen. Vaultwarden blijft draaien tot `vault` bewezen stabiel is.
- Alle input van buiten is onbetrouwbaar (mail, uploads, EXIF, WebDAV-paden):
  valideren, size limits, path traversal voorkomen.
- Parameterized queries, CSRF-bescherming, strikte CSP, rate limiting op auth-endpoints.
- Alleen het platform zelf is bereikbaar voor clients, en uitsluitend via HTTPS. Postgres, S3,
  NATS, workers en monitoring zijn nooit direct bereikbaar van buiten het servernetwerk.

## 9. Bestaande referentiecode

Mijn eerdere project: `github.com/AlbertovanEckeveld/website` (PHP 8.2, SQLite).
Bevat al: agenda (ICS, sync), taken, notities met links/graph, documentarchief
met full-text + AI-zoeken (BM25 + embeddings), bestanden met deellinks,
mailclient (IMAP/SMTP, OAuth), passkeys/e-mail-login, gebruikers/groepen/rechten.
Gebruik dit als **functionele** referentie bij het porten, niet als technisch voorbeeld. Neem de styling wel over.

## 10. Roadmap

0. Fundament: server, Compose, Postgres, S3-koppeling, NATS, CI, back-ups
1. `identity` (vervangt Authentik)
2. `drive` + WOPI/Collabora (vervangt Nextcloud-bestanden)
3. `pim` + CalDAV/CardDAV
4. `photos` (vervangt Immich)
5. `assistant` (lokale LLM + RAG)
6. `nvr` (vervangt Frigate), HA-koppeling via MQTT
7. `vault` (vervangt Vaultwarden)
8. `mail` (vervangt Gmail)

Een fase is pas af als het "klaar als"-criterium uit het roadmapdocument gehaald is.

## 11. Huidige status

- Fase 0: 0.1 skelet af, 0.3 CI af. 0.2 NATS + outbox en 0.4 back-ups uitgesteld; back-ups
  moeten er zijn vóór er echte data in staat.
- Fase 1 – identity: **af** (2026-11-15), door mij getest. Accounts, passkeys, wachtwoord +
  TOTP, herstelcodes, sessies, beheer van gebruikers/groepen/apps, audit-log, mail
  (ADR 0007), OIDC-provider met amr/acr, profielclaims en back-channel logout (ADR 0008).
  Het deelmodel is ontworpen (ADR 0006) en wordt gebouwd in fase 2 met `drive`.
- Bezig met: **fase 2 – drive**. 2.1 af (ADR 0009). 2.2 delen + versies (ADR 0010),
  2.2b teammappen (ADR 0011) en 2.3a deellinks (ADR 0012) en 2.3b voorbeeldweergave (incl. markdown en PDF) en 2.3c zoeken op
  naam (ADR 0013) en 2.4 app-wachtwoorden + WebDAV (ADR 0014) en 2.5 Collabora/WOPI (ADR 0015, door mij werkend
  bevestigd) gebouwd en getest; wachten op
  mijn eigen test. Alle onderdelen van fase 2 zijn gebouwd; wacht op mijn eindtest.
  Stappen: 2.1 drive-basis (mappen, streaming upload/download,
  hernoemen, verplaatsen, prullenbak, quota, bestandsbrowser); 2.2 versies + delen met
  gebruikers/groepen (deelmodel ADR 0006); 2.2b teammappen die beheerders aanmaken voor groepen; 2.3 deellinks, voorbeeldweergave, zoeken;
  2.4 app-wachtwoorden + WebDAV; 2.5 WOPI/Collabora; 2.6 desktop-synchronisatieclient
  (technologie nog te kiezen); zoeken op inhoud groeit mee (2.3c, uitgebreid zodra er
  tekstextractie is).
- Drive-verbeteringen (2026-11-17/18): A smalle schermen (container queries, ⋯-menu per rij) en
  B accountpagina (pagineren, gekoppelde computers, ADR 0021) gebouwd en getest; C versiebeheer
  en D rechten/instellingen/identity-pagina volgen, E AI alleen als ADR.
- Fase 2.6 desktop-sync: Go + Wails v2, eerst Linux en macOS, volledige kopie van gekozen mappen,
  inloggen via browser (OIDC + PKCE, loopback). 2.6a server, 2.6b sync-engine en 2.6c app
  (module `desktop/`, ADR 0016) gebouwd en getest, ook tegen de echte server; wacht op mijn test. Desktop-deps: wails v2.17.1, fsnotify, go-keyring, fyne.io/systray,
  modernc.org/sqlite.
- Klaar als (fase 2): uploaden/downloaden in browser en via WebDAV-client, delen met
  gebruiker/groep/link, versies terugzetten, prullenbak, document bewerken in Collabora,
  **een desktop-applicatie die foutloos uploadt en synchroniseert** (twee richtingen, met
  conflictafhandeling, ook na offline werken) en **een goede zoekoplossing** (snel, op naam én
  inhoud, binnen alles waar je toegang toe hebt). Zonder die twee is `drive` niet af.
- Testen: integratie- en browsertests draaien tegen een aparte database (`TEST_DATABASE_URL`,
  `ave_cloud_test`), nooit tegen de dev-database met mijn eigen account. Tests ruimen ook daar
  alleen hun eigen data op (`it-*`, `e2e-*`).
- Mail: SMTP-gegevens staan in `.skills/mail.md` en `.env`. Stuur geen echte mail naar
  buiten zonder mijn akkoord; tests gebruiken een nep-SMTP-server.
- Staging: `deploy/` voor `http://ave-dev-cloud.albertove.cloud:8080` (ADR 0019). Traefik er
  voorlopig uit: http via NetBird, ontwikkelmodus, geen passkeys; Collabora is de bestaande op
  `:9980`. Eerste start door mij (de assistent heeft geen Docker-toegang).
- Fase 0.3 CI: GitHub Actions draait (`ci.yml` op PR's). Uitrollen (ADR 0020): `release.yml`
  bouwt na een groene CI op `main` een cosign-ondertekend image in GHCR; de VM haalt het op
  via `deploy/update.sh` + systemd-timer. Release en VM-kant nog niet end-to-end getest (pas na
  de eerste merge naar `main`).
- Open vragen: definitieve keuze Go vs. Rust (huidige keuze: Go)

## 12. Beslissingen

- Geen eigen S3-objectopslag bouwen; bestaande S3-server gebruiken.
- Home Assistant en Collabora blijven extern en worden gekoppeld. Collabora (dev) draait op
  `ave-dev-cloud.albertove.cloud:9980` (andere NetBird-host); platform-instelling `COLLABORA_URL`.
- Frontend: SvelteKit met `adapter-static`, ingebed in de Go-binary (één origin, geen Node in productie).
- Go-dependencies fase 0: chi, pgx/v5, goose, minio-go, google/uuid. Frontend: SvelteKit, Vite, Vitest.
- CI: GitHub Actions. Uitrollen alleen pull-based: de VM start alleen images die cosign
  (keyless) als gebouwd door `release.yml` op `main` herkent; GitHub krijgt geen toegang tot de
  VM (ADR 0020). Actions vastgezet op commit-SHA.
- Inloggen: passkey als hoofdroute; wachtwoord alleen samen met een tweede factor (TOTP of passkey); herstelcodes.
- Eerste admin via CLI (`platform user create --admin`), die een eenmalige activatielink geeft.
- OIDC-provider op basis van `zitadel/oidc` (OpenID-gecertificeerd).
- Go-dependencies fase 1: golang.org/x/crypto (argon2id), go-webauthn/webauthn, zitadel/oidc.
- Go-dependencies fase 2: golang.org/x/net/webdav (WebDAV klasse 1+2, ook LOCK), go-pdfium +
  wazero (PDF-tekst in een WebAssembly-sandbox, zonder bestandssysteem).
- Frontend-dependencies fase 2: pdfjs-dist (Apache-2.0, PDF-lezer), marked (MIT, alleen als
  markdown-parser; renderen gebeurt in Svelte zonder ruwe HTML).
- Staging en later productie draaien achter Traefik (TLS met Let's Encrypt via DNS-01, geen
  Docker-socket), Collabora op hetzelfde adres (ADR 0019); voorlopig opgeschort, staging draait
  op http via NetBird. Geen WireGuard naast NetBird.
- Dev-adres: `http://albertove-msi.albertove.cloud:8443`, alleen op het NetBird-adres
  (100.80.128.240, versleuteld door WireGuard). Voorlopig http: passkeys werken daar pas met
  HTTPS (eigen certificaat of Let's Encrypt via DNS-01; besluit volgt).
- Drive: standaard geen quota (beheerder kan per gebruiker instellen), max. 10 GB per bestand,
  prullenbak 30 dagen. Beheerders zien geen bestanden van anderen.
- Multi-site hot-hot: nu nog niet repliceren, wel alles multi-site-proof ontwerpen (zie §3); replicatie wordt een eigen fase.
