# ADR 0021: Gekoppelde computers en pagineren op de accountpagina

- Status: geaccepteerd
- Datum: 2026-11-18

## Context

De accountpagina moet per computer laten zien waar de desktop-app gekoppeld is: naam,
besturingssysteem, wanneer actief, wat hij synchroniseert, en per computer kunnen ontkoppelen.
Tot nu toe was de desktop-app één regel onder "Apps"; intrekken koppelde alle computers los.
Daarnaast liepen de lijsten op de accountpagina eindeloos door.

## Beslissing

1. **Eén refresh-token is één computer.** Bij vernieuwen wordt de rij in
   `identity.oidc_refresh_tokens` bijgewerkt, niet vervangen, dus het id blijft stabiel zolang de
   computer gekoppeld is. Elk access-token wijst ernaar (`refresh_token_id`); `Principal.DeviceID`
   geeft het door. Geen apart apparaat-id dat de app zelf kiest: dat zou niet aan het token
   gebonden zijn.
2. **Modulegrens.** `identity` bewaart naam, besturingssysteem en "laatst actief" (bijgewerkt
   bij gebruik, hoogstens één keer per minuut) en ontkoppelt (token weg, audit `device.unlinked`).
   `drive` bewaart per apparaat-id welke mappen het synchroniseert en wanneer de laatste ronde was
   (`drive.sync_devices`). De accountpagina vraagt beide op en voegt ze samen; geen module leest
   de tabellen van de ander. Rijen van ontkoppelde computers worden niet getoond en na 90 dagen
   zonder melding opgeruimd.
3. **De app meldt zichzelf**: eenmalig per inlog zijn naam (hostname, max. 64 tekens, geen
   stuurtekens) en besturingssysteem (`PUT /api/v1/device`), na elke ronde zijn mappen
   (`PUT /api/v1/drive/sync/device`). Alleen een apparaat-token mag dat; het wordt alleen getoond,
   nergens op vertrouwd. Mappen worden bij het tonen opnieuw getoetst aan wat de gebruiker nu mag
   zien.
4. **Pagineren.** Recente activiteit groeit onbeperkt en pagineert op de server met een cursor
   (keyset: het UUIDv7-id van de laatste regel, `?before=`), niet met een offset: nieuwe
   gebeurtenissen verschuiven dan geen pagina. Sessies, apps en computers zijn korte lijsten en
   worden in de browser gepagineerd (3 per pagina); activiteit 5 per pagina.

## Gevolgen

- "Apps" toont de desktop-app niet meer; die staat per computer onder "Gekoppelde computers".
- Een computer die nooit een ronde afmaakt, staat er met "Nog niet gesynchroniseerd".
