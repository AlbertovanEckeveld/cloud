# ADR 0011: Teammappen

- Status: geaccepteerd
- Datum: 2026-11-16

## Context

Naast delen vanuit je eigen bestanden (ADR 0010) is er behoefte aan mappen die van een groep
zijn in plaats van van één persoon, zoals de app "Group folders" in Nextcloud: "Familie",
"Administratie". Een beheerder maakt ze aan en bepaalt welke groepen erbij kunnen.

Het alternatief, een gewone map van de beheerder die hij deelt, is afgewezen. Dan telt alles
voor het quotum van de beheerder, raakt de map wees als dat account verdwijnt, en ziet de
beheerder alle inhoud. Dat laatste is in strijd met ADR 0006 (beheerders beheren accounts,
geen inhoud).

## Beslissing

1. **Een teammap is zelf eigenaar.** `drive.team_folders` heeft een eigen UUID; alle nodes in
   de teammap hebben die UUID als `owner_id`. Er is precies één hoofdmap (`root_id`). Daardoor
   werken quotum, prullenbak, "één eigenaar per boom" en verplaatsen alleen binnen de teammap
   zonder extra code. Gebruikers- en teammap-UUID's zijn UUIDv7 en botsen niet.
2. **Toegang loopt via het bestaande deelmodel.** Groepen krijgen een share op de hoofdmap
   (`view`/`edit`/`manage`), dus overerving, live groepslidmaatschap, 403/404 en de audit-log
   gelden ongewijzigd.
3. **Beheerders zien de inhoud niet.** Ze zien naam, groepen en verbruik, maar openen de
   teammap alleen als ze zelf in een gekoppelde groep zitten. Zichzelf in een groep zetten
   komt in de audit-log.
4. **De hoofdmap is van de beheerder.** Hernoemen, opheffen en groepen koppelen kan alleen via
   het beheer, met recente login (15 min). Leden met `manage` kunnen submappen en bestanden wel
   verder delen, zoals in hun eigen bestanden.
5. **Eigen prullenbak per teammap.** Wat een lid verwijdert, komt daar. Leden met `edit` zien
   en zetten terug; leden met `manage` verwijderen definitief of legen de prullenbak. Iets
   waarvan de map verdwenen is, komt terug in de hoofdmap.
6. **Opheffen gaat in twee stappen.** Opheffen zet de hoofdmap in de prullenbak (leden kunnen
   er meteen niet meer bij); terugzetten kan 30 dagen. Daarna, of na een expliciete tweede
   bevestiging, wordt alles definitief verwijderd en worden de shares ingetrokken. Eén klik mag
   nooit de bestanden van een hele groep vernietigen.
7. **Audit:** aanmaken, hernoemen, quotum, opheffen, terugzetten en definitief verwijderen
   (`teamfolder.*`), plus de groepskoppelingen als `share.granted`/`share.revoked`. Drive meldt
   dit via een hook van identity ná de commit; mislukt dat, dan wordt het gelogd.
8. **Quotum per teammap,** standaard onbeperkt.

## Gevolgen

- Namen van teammappen zijn uniek (hoofdletterongevoelig); aanmaken en hernoemen nemen een
  tabel-lock zodat twee beheerders niet tegelijk dezelfde naam maken.
- Bij multi-site geldt "laatste wint" voor naam en quotum; een dubbele naam na replicatie is
  cosmetisch en door een beheerder op te lossen.
