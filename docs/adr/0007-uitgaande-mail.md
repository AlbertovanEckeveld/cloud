# ADR 0007: Uitgaande mail via een outbox

- Status: geaccepteerd
- Datum: 2026-11-15

## Context

Activatielinks moesten tot nu toe met de hand worden doorgegeven, en gebruikers merkten niet
dat iemand anders iets aan hun account veranderde. De `mail`-module (fase 8) is ver weg;
versturen kan al wel via een bestaande SMTP-server.

## Beslissing

1. **Outbox-patroon in Postgres** (`core.mail_outbox`). Een mail wordt in **dezelfde transactie**
   als de actie in de wachtrij gezet: een teruggedraaide actie verstuurt niets, en een
   geslaagde actie verliest zijn mail niet als de mailserver even weg is.
2. **Dispatcher** in elke instantie: elke 5 seconden, met `FOR UPDATE SKIP LOCKED`, zodat
   meerdere instanties of sites tegelijk kunnen versturen zonder dubbele mails. Mislukt
   verzenden, dan volgt een nieuwe poging met exponentiële backoff (1, 2, 4, … minuten); na 8
   pogingen wordt de mail opgegeven en gelogd.
3. **De inhoud staat versleuteld** in de wachtrij (AES-GCM, gebonden aan de rij), want een
   activatielink is een geheim. Na verzending wordt de rij verwijderd.
4. **SMTP via de Go-standaardbibliotheek** (`net/smtp`, `crypto/tls`): geen nieuwe dependency.
   Directe TLS (poort 465, RFC 8314) is de standaard, STARTTLS kan; onversleuteld alleen in
   development. TLS 1.2 is het minimum, en het certificaat wordt altijd gecontroleerd.
5. **Alleen platte tekst**, quoted-printable, met `Auto-Submitted: auto-generated` (RFC 3834).
   Elke header wordt op regeleinden gecontroleerd tegen header-injectie.
6. **Wat er gemaild wordt:** activatielinks bij uitnodigen en resetten, en beveiligingsmeldingen
   bij wachtwoord wijzigen, passkey toevoegen/verwijderen, nieuwe herstelcodes en inloggen met
   een herstelcode.
7. **Het e-mailadres zet alleen een beheerder.** Een gekaapte sessie kan meldingen dus niet
   omleiden. Er is bewust geen "wachtwoord vergeten per mail": herstel loopt via herstelcodes
   of een beheerder.

## Gevolgen

- Zonder `SMTP_HOST` staat mail uit; activatielinks staan dan alleen op het scherm.
- Bij het opstarten wordt de mailserver op de achtergrond gecontroleerd (verbinden + inloggen),
  zodat een fout wachtwoord meteen in de log staat.
- Dit is de eerste toepassing van het outbox-patroon; fase 0.2 (NATS) kan dezelfde tabelvorm
  gebruiken voor events.
