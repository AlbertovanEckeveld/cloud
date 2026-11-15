# ADR 0004: Authenticatie en sessies

- Status: geaccepteerd
- Datum: 2026-11-15

## Context

`identity` vervangt Authentik. Het platform is privé en hangt op termijn aan internet, dus
inloggen moet bestand zijn tegen credential stuffing, phishing, gestolen databasedumps en
session hijacking. Mail bestaat nog niet (fase 8), dus links kunnen niet gemaild worden.

## Beslissing

1. **Wachtwoord alleen samen met een tweede factor.** In 1.1 is TOTP (RFC 6238) verplicht;
   passkeys (WebAuthn) volgen in 1.2 als phishing-resistente hoofdroute. Een account zonder
   bevestigde tweede factor kan niet inloggen.
2. **Accounts worden geactiveerd via een eenmalige link** (72 uur, alleen de SHA-256 staat in
   de database). Het token staat in het URL-fragment (`#`), zodat het nooit in serverlogs of
   `Referer`-headers belandt. De eerste beheerder wordt via de CLI aangemaakt: wie geen
   toegang tot de server heeft, kan het platform niet "claimen". Een nieuwe link uitgeven
   reset het account; dat is ook de herstelroute.
3. **Wachtwoorden:** Argon2id met de parameters uit RFC 9106 §4 (64 MiB, t=3, p=4), opgeslagen
   als PHC-string zodat parameters later verhoogd kunnen worden (rehash bij inloggen).
   Maximaal twee hashes tegelijk, tegen geheugenuitputting. Beleid volgens NIST SP 800-63B:
   minstens 12 tekens, geen samenstellingsregels.
4. **Server-side sessies in Postgres**, geen JWT's. Een sessie is direct in te trekken
   (uitloggen, wachtwoord wijzigen, account-reset), en alle sites delen dezelfde tabel.
   - Cookie: 256-bit willekeurig token; in de database alleen de SHA-256.
   - `__Host-session`, `HttpOnly`, `Secure`, `SameSite=Lax`, `Path=/`.
   - Na het wachtwoord: een gedeeltelijke sessie (10 minuten) die alleen de tweede factor
     mag afronden. Bij elke stap omhoog krijgt de sessie een nieuw token (tegen session fixation).
   - Volledige sessie: verloopt na 7 dagen zonder gebruik en altijd na 30 dagen.
5. **TOTP-secrets versleuteld at rest** met AES-256-GCM (`ENCRYPTION_KEY`), gebonden aan de
   gebruiker via associated data. Gebruikte codes worden onthouden (geen replay), en
   verificatie gebeurt onder een rij-lock (geen race met dezelfde code).
6. **Throttling in Postgres**, gedeeld door alle instanties: 5 fouten per gebruikersnaam,
   20 per IP, 5 per TOTP, met een exponentieel oplopende blokkade tot 15 minuten. Na 5
   foute codes vervalt ook de gedeeltelijke sessie.
7. **Geen user enumeration:** een onbekende gebruiker kost evenveel tijd (dummy-hash) en
   geeft dezelfde foutmelding als een fout wachtwoord.
8. **CSRF:** `http.CrossOriginProtection` (Go-standaardbibliotheek, gebaseerd op
   `Sec-Fetch-Site`/`Origin`), plus `SameSite=Lax` en verplicht `application/json` op
   API-requests.
9. **Audit-log** in `identity.audit_events`, in dezelfde transactie als de actie waar dat
   kan. Geen foreign keys, zodat de geschiedenis een verwijderd account overleeft.

## Afgewogen alternatieven

- **JWT als sessietoken:** stateless, maar niet in te trekken zonder alsnog een denylist in
  de database. Geen voordeel voor een platform met één database.
- **bcrypt:** bewezen, maar niet memory-hard en beperkt tot 72 bytes invoer.
- **Setup-token in de log voor de eerste admin:** handiger, maar iedereen met toegang tot de
  logs kan dan beheerder worden.

## Gevolgen

- `ENCRYPTION_KEY` kwijt betekent dat alle gebruikers hun authenticator-app opnieuw moeten
  koppelen (via een nieuwe activatielink). Bewaar de sleutel in een wachtwoordmanager.
- Het client-IP komt nu uit de TCP-verbinding. Komt er later een reverse proxy voor, dan
  moet die het echte IP doorgeven en moet het platform alleen die proxy vertrouwen;
  anders delen alle gebruikers één IP-teller.
