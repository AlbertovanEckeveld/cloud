# ADR 0015: Documenten bewerken met Collabora Online (WOPI)

- Status: geaccepteerd
- Datum: 2026-11-16

## Context

Fase 2 eist dat documenten in de browser te bewerken zijn. Collabora Online (zelfgehost) blijft
extern (CLAUDE.md); `drive` is de WOPI-host: Collabora haalt documenten op en slaat ze op via
het WOPI-protocol.

## Beslissing

1. **Configuratie:** `COLLABORA_URL` (alleen scheme, host, poort). De editor-URL's uit
   Collabora's discovery moeten op precies die origin staan; anders weigert het platform
   bewerken met een duidelijke logmelding. Die origin is het enige dat de CSP van de app als
   iframe (`frame-src`) en formulierdoel (`form-action`) toestaat.
2. **Toegangstoken:** stateless, HMAC-SHA256 met een eigen sleutel (HKDF uit `ENCRYPTION_KEY`,
   apart van de OIDC-sleutel), gebonden aan één gebruiker, één bestand, lezen of schrijven, en
   8 uur geldig. Elke WOPI-aanroep controleert het token én opnieuw de rechten in drive:
   wie intussen geen toegang meer heeft, kan ook met een geldig token niets.
3. **Het token komt nooit in een URL van de app:** de browser post het in een formulier naar de
   iframe (WOPI-voorschrift); alleen Collabora zet het in zijn aanroepen naar ons.
4. **Proof keys:** publiceert Collabora een proof key, dan wordt elke aanroep op handtekening
   en ouderdom (20 minuten) gecontroleerd; zonder key alleen op het token.
5. **Opslaan = nieuwe versie** (met wie het deed). Is het bestand intussen anders gewijzigd
   (andere upload, WebDAV, tweede editor), dan klopt Collabora's tijdstempel niet en volgt
   409 met `COOLStatusCode 1010`, zodat Collabora de gebruiker laat kiezen.
6. **Niet aangeboden:** locks, hernoemen en "opslaan als" vanuit de editor (CheckFileInfo
   meldt dat); die lopen via drive zelf.
7. Berichten van de editor (`postMessage`) worden alleen van Collabora's origin geaccepteerd.

## Gevolgen

- Collabora moet het platform kunnen bereiken (`aliasgroup1` = `PUBLIC_URL`) en de browser
  moet Collabora kunnen bereiken op `COLLABORA_URL`.
- Zonder proof keys hangt de beveiliging op het token; Collabora kan proof keys genereren
  (`/etc/coolwsd/proof_key`), aan te raden voor productie.
