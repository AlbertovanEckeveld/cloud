# ADR 0013: Zoeken in drive (eerste stap: op naam)

- Status: geaccepteerd (uitgebreid met zoeken op inhoud: ADR 0017)
- Datum: 2026-11-16

## Context

`drive` is pas af met een goede zoekoplossing (CLAUDE.md): snel, op naam én inhoud, binnen
alles waar je toegang toe hebt. Zoeken op inhoud vraagt tekstextractie (PDF, Office) die er
nog niet is; deze stap levert zoeken op naam.

## Beslissing

1. **PostgreSQL met `pg_trgm`,** geen aparte zoekmachine. Een GIN-trigramindex op
   `lower(name)` (alleen niet-verwijderde items) maakt `LIKE '%deel%'` snel. `pg_trgm` hoort bij
   PostgreSQL en is een trusted extensie (de database-eigenaar mag hem installeren). Een aparte
   dienst (Meilisearch, OpenSearch) zou een extra proces met eigen kopie van de data en eigen
   rechtenlogica zijn; dat is pas zinvol als Postgres-full-text en pgvector (fase 5) tekortschieten.
2. **Rechten zitten in de zoekopdracht zelf.** Kandidaten komen alleen uit je eigen boom en uit
   bomen van eigenaren die iets met je delen; per kandidaat wordt het pad omhoog bepaald en
   dezelfde rechtencheck gedaan als bij openen. Iets in de prullenbak (ook via een map erboven)
   wordt nooit gevonden. Bij een share wordt de locatie van de eigenaar niet getoond.
3. **Rangschikking:** exacte naam, dan begint-met, dan kortere namen en recent gewijzigd.
   Maximaal 50 resultaten, minstens 2 tekens; LIKE-jokertekens in de invoer zijn letterlijk.

## Gevolgen

- Zoeken op inhoud volgt zodra er tekstextractie is (media-/ml-worker); dan komen Postgres
  full-text (`tsvector`, Nederlands) en later semantisch zoeken met pgvector erbij.
- Per zoekopdracht worden tot 500 naamkandidaten op toegang gecontroleerd; dat volstaat voor
  een huishouden en is te verhogen of te indexeren als het ooit tekortschiet.
