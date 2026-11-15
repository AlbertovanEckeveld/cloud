# ADR 0010: Delen met gebruikers en groepen, en versies

- Status: geaccepteerd
- Datum: 2026-11-15

## Context

Fase 2.2 bouwt het deelmodel uit ADR 0006 met `drive` als eerste gebruiker, en maakt de
versies uit ADR 0009 zichtbaar. CLAUDE.md eist één centrale autorisatiecheck in `identity`;
modules mogen elkaars tabellen niet lezen.

## Beslissing

### Deelmodel

1. **Shares staan in `identity.grants`**, per resource (`drive.node` + UUID) en per subject
   (gebruiker of groep), met `view` < `edit` < `manage`. Drive en identity delen alleen het kleine
   package `internal/core/authz` (types + interface `Authorizer`); drive importeert identity niet.
2. **Eigenaarschap blijft in de module.** De eigenaar van een bestand heeft impliciet `manage`;
   daar is geen rij voor nodig. Dat wijkt af van het voorstel in ADR 0006 ("de maker krijgt
   `manage`"): eigenaarschap is een eigenschap van het item, geen share, en kan zo nooit per
   ongeluk worden ingetrokken.
3. **Rechten erven naar beneden.** `authorize()` zoekt de keten van het item naar boven en vraagt
   identity in één query naar alle shares op die keten; het sterkste recht telt. Groepslidmaatschap
   wordt bij elke check opgezocht, dus uit een groep halen of uitschakelen werkt meteen.
4. **Niet zichtbaar = 404, wel zichtbaar maar te weinig recht = 403.** Wie iets kan zien, weet al
   dat het bestaat; een 403 lekt dan niets en geeft een begrijpelijke fout.
5. **De mappen van de eigenaar boven een share blijven privé.** Het pad (broodkruimels) begint bij
   de gedeelde map, en van de gedeelde map zelf wordt `parent_id` niet doorgegeven.
6. **Een boom heeft één eigenaar.** Wat iemand met `edit` in een gedeelde map zet, is van de
   eigenaar van die map en telt voor diens quotum; de versie legt vast wie het uploadde.
   Verplaatsen kan alleen binnen de mappen van dezelfde eigenaar, en verwijderen gaat naar de
   prullenbak van de eigenaar. Alternatief was "de uploader blijft eigenaar" (Google Drive): dat
   geeft gemengde bomen, quota die niemand overziet en mappen die je niet kunt opruimen.
7. **Alleen `manage` mag delen**, nooit met de eigenaar of met jezelf. Iedereen mag een share met
   zichzelf opheffen ("Verlaten"); een groepsshare kan alleen een beheerder van het item intrekken.
8. **Elke deelactie gaat naar de audit-log** (`share.granted`, `share.revoked`).
9. **Opruimen:** shares van definitief verwijderde items worden na de commit ingetrokken. Dat kan
   niet in dezelfde transactie (andere module); een achtergebleven share wijst naar niets en geeft
   geen toegang, omdat UUID's nooit worden hergebruikt. Een verwijderde groep neemt zijn shares mee.
10. **Adresboek:** iedere ingelogde gebruiker kan actieve gebruikers en groepen zoeken
    (`/api/v1/directory`, max. 20 per soort), omdat delen dat nodig heeft. Voor een
    huishouden/familie is dat de juiste afweging; bij grotere installaties zou dit per groep
    afgeschermd moeten worden.

### Versies

1. Elke upload van een bestaande naam maakt een nieuwe versie (ADR 0009). De geschiedenis is te
   zien en elke versie te downloaden (`view`).
2. **Terugzetten** (`edit`) maakt een oude versie weer de huidige door ernaar te verwijzen, niet
   door haar te kopiëren: objecten zijn onveranderlijk, dus dat kost geen opslag, en de hele
   geschiedenis blijft staan zodat ook het terugzetten zelf ongedaan kan worden.
3. **Een oude versie verwijderen** vraagt `manage`, omdat geschiedenis wissen destructief is. De
   huidige versie kan niet worden verwijderd. Terugzetten en verwijderen nemen dezelfde rij-lock,
   zodat de huidige versie nooit naar een verwijderde versie kan wijzen.

## Gevolgen

- Versies worden nog niet automatisch opgeruimd en tellen mee voor het quotum. Een bewaarbeleid
  (bijv. zoals Nextcloud: steeds dunner over de tijd) is een latere beslissing.
- `SharedWithMe` doet per share een autorisatiecheck (N+1 queries); prima voor een huishouden,
  te optimaliseren als het ooit traag wordt.
- Multi-site: shares en versies zijn gewone rijen met UUIDv7; een gelijktijdige wijziging van
  hetzelfde recht op twee sites wordt "laatste wint", wat hier acceptabel is.
