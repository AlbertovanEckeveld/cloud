# ADR 0005: Passkeys, herstelcodes en recente authenticatie

- Status: geaccepteerd
- Datum: 2026-11-15

## Context

Wachtwoord + TOTP (ADR 0004) is phishing-gevoelig: een nagemaakte loginpagina kan beide
doorgeven aan de echte site. Passkeys (WebAuthn Level 3, FIDO2) zijn gebonden aan het domein
en daardoor phishing-resistent.

## Beslissing

1. **Passkeys als volwaardige login**, via `github.com/go-webauthn/webauthn` (BSD-3).
   - Discoverable credentials (`residentKey: required`): inloggen zonder gebruikersnaam.
   - `userVerification: required`: elke keer vingerafdruk, gezicht of pincode. Bezit +
     biometrie/kennis telt daarom als twee factoren; de login levert direct een volledige sessie.
   - `attestation: none`: we vragen niet welk merk authenticator het is (privacy, en we
     vertrouwen geen specifieke fabrikanten).
   - User handle = de UUID van het account: niet-persoonlijk, zoals WebAuthn §14.6.1 aanraadt.
   - Relying Party ID = hostnaam van `PUBLIC_URL`. WebAuthn weigert IP-adressen, dus
     `PUBLIC_URL` moet een hostnaam zijn (lokaal: `localhost`).
2. **Ceremonies staan in Postgres** (`identity.webauthn_challenges`): eenmalig (verwijderd bij
   gebruik), 5 minuten geldig, registratie gebonden aan de sessie die hem startte. Zo kan
   elke instantie of site een ceremonie afronden die een andere begon.
3. **Clone-detectie:** daalt de signature counter (WebAuthn §6.1.1), dan wordt de login
   geweigerd en gelogd. Gesynchroniseerde passkeys gebruiken altijd 0 en vallen hierbuiten.
4. **Herstelcodes:** 10 codes van 80 bits, alleen als SHA-256 opgeslagen (bij 80 bits
   entropie is een trage hash niet nodig), eenmalig te gebruiken, vervangen alleen de
   TOTP-stap. Ze worden direct na activatie getoond.
5. **Recente authenticatie** (15 minuten) is vereist voor passkeys toevoegen/verwijderen en
   herstelcodes vernieuwen. Een gestolen sessiecookie levert zo geen blijvende toegang op.
6. **Sessiebeheer:** gebruikers zien hun ingelogde apparaten en kunnen ze afzonderlijk of
   allemaal tegelijk uitloggen.
7. `platform user reset` wist ook passkeys en herstelcodes.

## Gevolgen

- Tijdens `make dev` draait de frontend op `localhost:5173`; die origin wordt in
  development extra toegestaan voor passkey-ceremonies.
- Zonder recente login krijg je bij een gevoelige wijziging de vraag opnieuw in te loggen.
