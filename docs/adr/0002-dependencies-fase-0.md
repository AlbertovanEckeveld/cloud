# ADR 0002: Dependencies fase 0

- Status: geaccepteerd
- Datum: 2026-11-15

## Beslissing

Uitgangspunt: standaardbibliotheek waar het kan; daarnaast alleen breed gebruikte,
actief onderhouden libraries met een permissieve licentie.

### Go

| Library | Licentie | Waarom |
|---|---|---|
| `github.com/go-chi/chi/v5` | MIT | Router met middleware en subrouters, gebouwd op `net/http`-types. |
| `github.com/jackc/pgx/v5` | MIT | De standaard Postgres-driver voor Go: connection pool en native types. |
| `github.com/pressly/goose/v3` | MIT | SQL-migraties als bestanden, als library aan te roepen, één versietabel per schema. |
| `github.com/minio/minio-go/v7` | Apache-2.0 | S3-client die lichter is dan de AWS SDK v2 en goed werkt met zelfgehoste S3. |
| `github.com/google/uuid` | BSD-3-Clause | UUIDv7 (RFC 9562) voor IDs en request-ID's. |

Alternatieven die zijn afgevallen: `golang-migrate` (minder fijne Go-API), AWS SDK v2
(zwaar, veel indirecte dependencies) en `database/sql` met `lib/pq` (onderhoudsmodus).

### Frontend (alleen build-time; de browser krijgt gecompileerde bestanden)

| Package | Licentie | Waarom |
|---|---|---|
| SvelteKit + `adapter-static` | MIT | Routing en build voor een single-page app; de output wordt in de Go-binary ingebed. |
| Svelte 5 | MIT | Compileert naar kleine, snelle JavaScript zonder virtuele DOM. |
| Vite | MIT | Build- en devserver. |
| Vitest | MIT | Unit tests met dezelfde configuratie als Vite. |
| TypeScript, svelte-check | Apache-2.0 / MIT | Typecontrole. |

## Bekende bevindingen

- `npm audit` meldt "low" voor `deepmerge` (prototype poisoning) via
  `@sveltejs/vite-plugin-svelte`. Dit draait alleen tijdens de build, op onze eigen
  configuratie, en komt niet in de browser. Er is nog geen gepatchte versie; geaccepteerd
  tot er een upstream-fix is.
