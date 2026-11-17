# ADR 0012: Openbare deellinks

- Status: geaccepteerd
- Datum: 2026-11-16

## Context

Fase 2.3 voegt links toe waarmee iemand zonder account een bestand of map kan bekijken en
downloaden (ADR 0006: eigen token, alleen de hash opgeslagen, verloopdatum, optioneel
wachtwoord, alleen `view`).

## Beslissing

1. **Token:** 256 bits uit `crypto/rand`, base64url. Opgeslagen wordt alleen SHA-256 van de
   token; een gestolen database geeft geen bruikbare links. Een snelle hash volstaat hier, omdat
   de token zelf niet te raden is (anders dan een wachtwoord). De token wordt één keer getoond.
2. **De token komt nooit in een URL-pad of query.** De link is `/s#<token>`: browsers sturen het
   fragment niet naar de server, en de pagina haalt het meteen uit de adresbalk. De API krijgt de
   token alleen in de body (`POST .../public/links/open` als JSON, downloaden als klein
   formulier), zodat hij niet in toegangslogs, proxylogs, browsergeschiedenis of `Referer`
   belandt. De pagina zet bovendien `Referrer-Policy: no-referrer`.
3. **Alleen lezen.** Een maplink toont de map en alles eronder; elke opgevraagde map of elk
   bestand wordt gecontroleerd op "ligt onder de gelinkte map en niet in de prullenbak".
   Downloads krijgen dezelfde bescherming als gewone downloads (attachment, sandbox-CSP).
4. **Wachtwoord:** optioneel, minimaal 8 tekens, argon2id met dezelfde kosten als
   accountwachtwoorden. Drive hasht zelf (de hasher van identity is intern); er lopen hooguit
   twee berekeningen tegelijk, en alleen na een geldige token, zodat het publieke endpoint geen
   geheugen- of CPU-aanval mogelijk maakt.
5. **Brute force:** na 10 foute wachtwoorden zit de link 15 minuten op slot, ook voor het juiste
   wachtwoord. Daarna geeft elke volgende fout opnieuw 15 minuten (de teller wordt pas na een
   goed wachtwoord gereset).
6. **Geen informatie lekken:** een onbekende, verlopen, ingetrokken of in de prullenbak gezette
   link geeft steeds dezelfde 404.
7. **Beheer:** alleen wie `manage` heeft op het item maakt of trekt links in; aanmaken en
   intrekken staan in de audit-log (`link.created`, `link.deleted`). Een link verdwijnt mee met
   het item (foreign key).

## Gevolgen

- Een link geeft geen sessie of cookie; elke aanvraag controleert token (en wachtwoord) opnieuw.
  Bij een wachtwoordlink kost elke map of download dus één argon2-berekening; voor huishoudelijk
  gebruik is dat prima.
- Voorbeeldweergave (2.3b) en zoeken (2.3c) volgen als aparte stappen.
