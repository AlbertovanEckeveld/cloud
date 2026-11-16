# ADR 0018: Activiteit en voortgang

- Status: geaccepteerd
- Datum: 2026-11-16

## Context

Gebruikers willen zien wát er gesynchroniseerd wordt, op de server en in de desktop-app, en
hoe lang een upload nog duurt.

## Beslissing

1. **Activiteit op de server** komt uit het bestaande wijzigingslog (ADR 0016). De trigger legt
   nu ook de actie vast (nieuw, gewijzigd, hernoemd, verplaatst, prullenbak, teruggezet,
   verwijderd), wie het deed en via welk kanaal: webapp, desktop-app, WebDAV of
   documenteditor. Het kanaal zet de code die de wijziging doet (`updated_via`), afgeleid van
   hoe de aanvraag binnenkwam (sessie, apparaattoken, `/dav`, WOPI).
2. Het overzicht toont alleen wat de gebruiker nu mag zien (dezelfde check als openen), plus
   eigen verwijderingen; een nieuw bestand is één regel. Het gaat 30 dagen terug, net als het log.
3. **De desktop-app** meldt het lopende bestand (met stap x van y) en houdt de laatste 100
   stappen bij; het systeembalk-icoon toont het lopende bestand.
4. **Tijdsinschatting:** uit de gemiddelde snelheid sinds de start van de overdracht, afgerond
   ("nog ~2 min"), pas na een seconde. Webapp en desktop-app gebruiken dezelfde functie.

## Gevolgen

- Definitief verwijderen heeft geen "door wie" meer (de rij bestaat niet meer).
