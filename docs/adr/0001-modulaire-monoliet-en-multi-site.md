# ADR 0001: Modulaire monoliet, ontworpen voor meerdere sites

- Status: geaccepteerd
- Datum: 2026-11-15

## Context

Het platform vervangt een reeks losse apps (Authentik, Nextcloud, Immich, …) en draait
thuis op bescheiden hardware. Op termijn moet het op twee of meer sites draaien die
elkaar actief-actief (hot-hot) synchroniseren en bij uitval overnemen.

## Beslissing

1. **Eén Go-binary (modulaire monoliet).** Modules zijn packages met een smalle publieke
   interface en een eigen Postgres-schema met dezelfde naam. Een module raakt nooit de
   tabellen van een andere module aan. Microservices zouden netwerk, deployment en
   debugging veel complexer maken, zonder voordeel bij één gebruiker en één server.
2. **Elke module beheert zijn eigen migraties** (`migrations/<module>/`). Migraties draaien
   bij het opstarten onder een Postgres advisory lock, zodat bij gelijktijdig starten
   precies één instantie migreert.
3. **Replicatie tussen sites bouwen we nu nog niet**, maar elk ontwerp blijft er geschikt voor:
   - App-processen zijn stateless; alle state staat in Postgres, S3 of (later) NATS.
   - IDs zijn UUIDv7 en worden in Go gegenereerd, nooit door database-sequences die
     tussen sites zouden botsen.
   - Elke instantie heeft een `SITE_ID`. Instanties registreren zich in `core.sites` met
     een heartbeat, zodat zichtbaar is welke sites leven en welke versie ze draaien.
   - S3-objecten worden nooit overschreven: een nieuwe versie krijgt een nieuwe key.
   - Events worden idempotent verwerkt en via een outbox gepubliceerd (fase 0.2).

## Afweging replicatie (voor later)

| Optie | Voor | Tegen |
|---|---|---|
| Postgres primary + standby (Patroni) | Bewezen, eenvoudig | Eén site schrijft (hot-standby, niet hot-hot) |
| Bidirectionele logical replication | Beide sites schrijven | Conflicten zelf oplossen; foutgevoelig |
| Gedistribueerde SQL (YugabyteDB) | Quorum ingebouwd | Zwaar voor de hardware, beperkte pgvector-steun |
| Applicatie-niveau (events + merge per domein) | Flexibel, leerzaam | Meeste werk |

Met twee sites is automatische failover nooit veilig: bij een netwerksplit denken beide
dat de ander weg is (split-brain). Er is een derde stem (witness) nodig.

## Gevolgen

- Iets dat alleen in het geheugen of op lokale schijf staat, is een bug.
- De keuze voor een replicatiemechanisme wordt een eigen fase met een eigen ADR.
