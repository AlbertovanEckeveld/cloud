# ADR 0022: Rollen en rechten in plaats van "beheerder ja/nee"

- Status: geaccepteerd
- Datum: 2026-11-18

## Context

Beheer is nu één vlag: `identity.users.is_admin`. Wie hem heeft, mag alles (gebruikers,
groepen, apps, audit-log, teammappen, quota, systeemstatus); wie hem niet heeft, niets. Gewenst is
fijnmaziger beheer ("mag teammappen maken, maar geen gebruikers"), drive-instellingen achter een
tandwiel op de drive-pagina, en een aparte pagina voor identity-beheer.

Alternatieven:

- **Vlaggen per gebruiker** (`can_manage_teamfolders`, …): eenvoudig, maar niet uit te delen aan
  een groep en elke nieuwe bevoegdheid is een migratie.
- **Policy-taal** (OPA/Cedar, ABAC): krachtig, maar een nieuwe dependency en veel meer dan dit
  platform nodig heeft; beheerregels worden lastig te controleren.
- **RBAC: vaste rechten, rollen als bundels, toegekend aan gebruikers of groepen.** Gekozen:
  bekend model (NIST RBAC), past op het bestaande groepenmodel en is goed te auditen.

Het deelmodel voor bestanden (ADR 0006: view/edit/manage per item) blijft zoals het is; dit gaat
alleen over **beheer**.

## Beslissing

### Rechten

Vaste namen in de code (niet in de database), `module.onderwerp.actie`:

| Recht | Mag |
|---|---|
| `identity.users.view` | gebruikers en hun activiteit zien |
| `identity.users.invite` | gebruikers uitnodigen |
| `identity.users.edit` | naam en e-mailadres wijzigen |
| `identity.users.disable` | uit- en inschakelen |
| `identity.users.reset` | wachtwoord, tweede factor en sessies intrekken (nieuwe activatielink) |
| `identity.groups.view` | groepen en leden zien |
| `identity.groups.manage` | groepen maken, wijzigen, verwijderen, leden beheren |
| `identity.roles.view` | rollen en wie ze heeft zien |
| `identity.roles.manage` | eigen rollen maken en wijzigen |
| `identity.roles.assign` | rollen toekennen en intrekken |
| `identity.apps.manage` | OIDC-apps registreren en beheren |
| `identity.audit.view` | de volledige audit-log zien |
| `drive.teamfolders.view` | alle teammappen zien (niet de inhoud) |
| `drive.teamfolders.create` | teammappen maken |
| `drive.teamfolders.manage` | hernoemen, groepen en quotum instellen |
| `drive.teamfolders.delete` | opheffen, terugzetten, definitief verwijderen |
| `drive.quota.manage` | gebruik en quotum per gebruiker zien en instellen |
| `system.status.view` | systeemstatus en sites zien |

Een nieuw recht toevoegen is een codewijziging, nooit een databasewijziging.

### Rollen

- `identity.roles`: id (UUIDv7), naam, omschrijving, lijst van rechten, `builtin`.
- `identity.role_assignments`: rol + gebruiker **of** groep. Groepslidmaatschap wordt live
  meegeteld, net als bij delen.
- **Ingebouwde rol "Beheerder"** heeft altijd álle rechten, ook rechten die later bijkomen. Hij is
  niet te wijzigen of te verwijderen.
- **Migratie:** elke gebruiker met `is_admin` krijgt de rol Beheerder, daarna verdwijnt de kolom.
  `platform user create -admin` kent voortaan deze rol toe.

### Controle

- Bij elke request laadt `RequireUser` de effectieve rechten van de gebruiker in de `Principal`
  (één query; niets in geheugen of cookies, zodat het multi-site blijft werken en een intrekking
  direct telt).
- Routes vragen een recht: `RequirePermission("drive.teamfolders.create")`. Er blijft één
  centrale plek in `identity` die beslist (CLAUDE.md §7); `drive` krijgt die functie aangereikt,
  zoals nu `RequireAdmin`.
- De desktop-app (apparaat-token) heeft nooit beheerrechten, ook niet als de gebruiker ze heeft.
- Wijzigingen aan rollen en toekenningen vragen een recente inlog en komen in de audit-log.

### Geen escalatie

Wie beheerrechten uitdeelt, mag nooit meer geven dan hij zelf heeft:

1. **Toekennen:** een rol toekennen kan alleen als je zelf álle rechten van die rol hebt.
2. **Rollen wijzigen:** je kunt alleen rechten aan een rol toevoegen die je zelf hebt.
3. **Groepslidmaatschap is ook toekennen:** iemand toevoegen aan een groep die een rol heeft, valt
   onder regel 1 (anders is `identity.groups.manage` een omweg naar Beheerder).
4. **Accounts van zwaardere gebruikers:** resetten, uitschakelen of het e-mailadres wijzigen kan
   alleen bij gebruikers die geen rechten hebben die jij mist. Anders kan iemand met
   `identity.users.reset` een beheerder resetten en via de activatielink diens account overnemen.
5. **Er blijft altijd minstens één actieve gebruiker met de rol Beheerder** (vervangt de huidige
   "laatste beheerder"-regel).

### Indeling van de schermen

- **Bestanden**: een tandwiel (alleen zichtbaar met een `drive.*`-beheerrecht) opent
  `/files/settings` met de tabbladen Teammappen en Opslag per gebruiker.
- **Identity** (nieuwe hoofdpagina, alleen met een `identity.*`-recht): Gebruikers, Groepen,
  Rollen (een matrix van rollen × rechten), Apps, Audit-log. Rollen ken je toe op de pagina van een
  gebruiker of groep.
- **Systeem** (met `system.status.view`): de huidige systeemstatus en sites.
- "Beheer" verdwijnt. De frontend krijgt de lijst rechten in de sessie en toont alleen wat mag;
  de server controleert elk verzoek opnieuw.

### Uitvoering in drie stappen

1. **D1 backend**: tabellen, migratie van `is_admin`, `RequirePermission`, alle routes omgezet,
   escalatieregels, API voor rollen. Integratietests per recht en per escalatieregel.
2. **D2 Identity-pagina**: gebruikers, groepen, rollen-matrix, toekennen, apps, audit-log.
3. **D3 Drive-tandwiel** en de Systeem-pagina; "Beheer" weg.

## Gevolgen

- Eén extra query per request (rechten laden); met een index op de toekenningen is dat
  verwaarloosbaar naast de sessie-query die er al is.
- De groepen worden belangrijker: een groep met een rol is een beheerdersgroep. De groepspagina
  laat dat zien.
- `is_admin` verdwijnt uit de API; de sessie geeft `permissions: [...]`.
