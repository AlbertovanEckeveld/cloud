# ADR 0020: Uitrollen via ondertekende images (pull)

- Status: geaccepteerd
- Datum: 2026-11-17

## Context

Staging (ADR 0019) moet automatisch bijwerken na een push naar `main`. De repo is publiek en de
VM is alleen via NetBird bereikbaar.

Alternatieven:

- **Self-hosted runner op de VM.** Afgewezen: bij een publieke repo kan code uit pull requests
  op de runner draaien, en de runner heeft Docker-rechten (= root).
- **Runner joint NetBird en deployt via SSH.** Afgewezen: een gecompromitteerde workflow of een
  gelekt geheim geeft dan toegang tot het mesh-netwerk en de VM.
- **Pull: de VM haalt een ondertekend image.** Gekozen. GitHub heeft geen enkel geheim dat
  toegang geeft tot de VM.

## Beslissing

1. **CI** (`ci.yml`) draait op pull requests: `make check`, `make test`, `make test-integration`
   tegen een tijdelijke Postgres en RustFS. Alleen leesrechten, geen geheimen, actions vastgezet
   op commit-SHA.
2. **Release** (`release.yml`) draait bij een push naar `main`: eerst dezelfde CI, en alleen als
   die slaagt bouwt hij het image, pusht het naar `ghcr.io/albertovaneckeveld/cloud` (`:main` en
   `:sha-…`) en ondertekent het **keyless met cosign** (Sigstore). Er bestaat geen
   ondertekeningssleutel: de workflow ruilt zijn OIDC-token voor een kortlevend certificaat
   waarin staat welke workflow en welke ref tekende, en dat wordt vastgelegd in het publieke
   transparantielog (Rekor).
3. **De VM** (`deploy/update.sh` via een systemd-timer) zoekt de digest achter `:main`, laat
   cosign die digest controleren tegen precies `release.yml@refs/heads/main` van deze repo, en
   start dan die digest (niet de tag), zodat controleren en starten over hetzelfde image gaan.
   Wordt hij niet `healthy`, dan gaat hij terug naar de vorige digest en onthoudt hij de
   afgekeurde.
4. Het GHCR-package is publiek (de code is dat ook; het image bevat geen geheimen), dus de VM
   heeft geen token nodig.
5. Wijzigingen in `deploy/` zelf (compose, instellingen) rolt de timer **niet** uit; dat blijft een
   handmatige `git pull`. Anders zou een push naar `main` alsnog willekeurige containers op de VM
   kunnen starten, buiten de handtekening om.

## Gevolgen

- Wie `main` kan beschrijven, kan uitrollen. Branch protection op `main` (pull request
  verplicht, CI verplicht) is daarom aan te raden.
- **Migraties lopen alleen vooruit.** Bij een terugval draait de vorige code op een al
  gemigreerd schema. Migraties moeten dus achterwaarts compatibel zijn (eerst toevoegen, pas in
  een latere release weghalen). Voor staging met een lege database is een mislukte migratie
  acceptabel; voor productie is dit een harde regel.
- De VM heeft `curl`, `cosign` (vaste versie met checksum) en Docker Compose nodig.
- cosign heeft bij het controleren internet nodig (Sigstore-vertrouwensroot, eenmalig gecachet).
