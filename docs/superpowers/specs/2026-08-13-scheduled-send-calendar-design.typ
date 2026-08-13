#import "@preview/fletcher:0.5.8": diagram, node, edge

#set document(title: "Ontwerp: geplande verzending en agenda voor mox", author: "mox")
#set page(paper: "a4", margin: (x: 20mm, y: 18mm), numbering: "1 / 1")
#set text(lang: "nl", size: 10pt)
#set par(justify: true, leading: 0.62em)
#set heading(numbering: "1.1")
#show heading.where(level: 1): it => [#pagebreak(weak: true)#it]
#show raw.where(block: false): set text(font: "DejaVu Sans Mono", size: 8.5pt)

#let product = smallcaps("mox")
#let req(body) = block(
  fill: rgb("f3f6fa"),
  stroke: rgb("9aacc0"),
  radius: 3pt,
  inset: 8pt,
  width: 100%,
  body,
)

#align(center)[
  #text(18pt, weight: "bold")[Ontwerp: geplande verzending en agenda]
  #v(4pt)
  #text(11pt)[Server-side scheduling, CalDAV en iCalendar-interoperabiliteit voor #product]
  #v(8pt)
  13 augustus 2026 · status: ter review
]

#v(12pt)

#req[
  *Harde compatibiliteitsvoorwaarden.* Alle bestaande IMAP- en SMTP-functies van
  #product blijven werken. Bestaande configuratiebestanden en hun formaat blijven
  ongewijzigd. Eventuele nieuwe instellingen komen uitsluitend in een apart,
  optioneel configuratiebestand voor geplande verzending en agenda.
]

= Samenvatting

Dit ontwerp voegt twee onafhankelijke functies toe:

- *Geplande verzending:* voortbouwen op de bestaande queue, SMTP
  `FUTURERELEASE` en webmailondersteuning. Een accountgerichte service en een
  virtuele IMAP-mailbox `Scheduled` maken geplande berichten zichtbaar en
  beheerbaar zonder een tweede bron van waarheid te introduceren.
- *Agenda:* één server-side standaardagenda per account, bereikbaar vanuit
  webmail, CalDAV en een mox-JSON-API. iCalendar/iTIP en e-mailtransport (iMIP)
  verzorgen uitnodigingen, reacties, wijzigingen en annuleringen voor interne
  en externe deelnemers.

Versie 1 ondersteunt terugkerende afspraken en uitzonderingen, maar geen
free/busy-opzoeking, planningsassistent, gedeelde agenda's, taken of server-side
herinneringen. Herinneringen zijn de verantwoordelijkheid van externe
CalDAV-clients.

= Doelen en niet-doelen

== Doelen

- Canonieke agenda- en schedulingdata blijven op de mox-server.
- Externe agenda-apps synchroniseren via CalDAV met iCalendar-resources.
- Gmail-, Outlook- en andere externe deelnemers wisselen uitnodigingen en
  reacties uit via standaard calendar-e-mail.
- Eén standaardagenda per account, met losse en terugkerende afspraken.
- Geplande berichten kunnen worden bekeken, geannuleerd, herpland, direct
  verzonden of teruggezet naar een concept.
- Alle statusovergangen zijn atomair, idempotent en herstelbaar na een crash.

== Niet-doelen voor versie 1

- Beschikbaarheid van andere gebruikers, free/busy en een planningsassistent.
- Meerdere, gedeelde of gedelegeerde agenda's.
- Taken (`VTODO`), resources en reserveringen.
- Server-side herinneringen of notificaties vóór een afspraak.
- Een gebruikersactie “dit en alle volgende” voor terugkerende afspraken.
- Plannen door een bericht via IMAP naar `Scheduled` te verplaatsen.

= Architectuur

De subsystemen delen bestaande account-, authenticatie-, opslag- en
mailinfrastructuur, maar hebben elk een duidelijke eigenaar. De delivery queue
blijft de bron van waarheid voor geplande mail. De calendar store is de bron van
waarheid voor afspraken. Geen IMAP-mailbox of iCalendar-tekstblob wordt een
tweede gezaghebbende kopie.

#figure(
  diagram(
    spacing: (7mm, 10mm),
    node-stroke: 0.8pt,
    node-fill: rgb("f7f9fc"),
    node((0, 0), [Webmail], name: <webmail>),
    node((0, 1), [Externe mailclient], name: <mailclient>),
    node((0, 2), [Externe agenda-app], name: <calclient>),
    node((2, 0), [ScheduledMessage-service], name: <scheduled>),
    node((2, 1), [SMTP submission / IMAP], name: <mailprotocols>),
    node((2, 2), [Calendar-API / CalDAV], name: <calendarapi>),
    node((4, 0), [Delivery queue], name: <queue>),
    node((4, 1), [Scheduling-domeinservice], name: <domain>),
    node((4, 2), [Calendar store + outbox], name: <store>),
    node((6, 1), [iTIP/iMIP-verwerker], name: <imip>),
    edge(<webmail>, <scheduled>, "->"),
    edge(<webmail>, <calendarapi>, "->"),
    edge(<mailclient>, <mailprotocols>, "->"),
    edge(<calclient>, <calendarapi>, "->"),
    edge(<scheduled>, <queue>, "->"),
    edge(<mailprotocols>, <queue>, "->"),
    edge(<calendarapi>, <domain>, "->"),
    edge(<domain>, <store>, "->"),
    edge(<store>, <imip>, "->"),
    edge(<imip>, <queue>, "->"),
  ),
  caption: [Hoofdcomponenten en gezaghebbende opslag.],
)

== Geplande verzending

- Een `ScheduledMessage` groepeert alle recipient queue-items van één logisch
  bericht onder een stabiele identifier.
- Webmail gebruikt een accountgerichte service voor list, get, cancel,
  reschedule, release-now en convert-to-draft.
- De virtuele speciale IMAP-mailbox `Scheduled` projecteert dezelfde logische
  berichten. Ze is geen fysieke mailbox en consumeert geen UID-ruimte van een
  bestaande mailbox.
- Verwijderen uit `Scheduled` annuleert alleen als vrijgave nog niet is gestart.
  Flags wijzigen heeft geen schedulingsemantiek. Kopiëren of verplaatsen naar
  `Scheduled` plant geen verzending.
- SMTP submission met RFC 4865 `FUTURERELEASE`, de webmail-API en de nieuwe
  accountservice schrijven naar dezelfde bestaande queue.

== Agenda

- Iedere accountdatabase krijgt één standaardagenda.
- De calendar store beheert events, series, uitzonderingen, deelnemers en
  schedulingmetadata.
- Een domeinservice valideert iedere mutatie en bepaalt welke uitnodiging,
  update, annulering of reactie nodig is.
- CalDAV en webmail-JSON-API zijn adapters op dezelfde domeinservice.
- De iTIP/iMIP-verwerker verwerkt calendar-MIME-parts en verstuurt uitgaande
  schedulingberichten via het bestaande mailpad.
- Een transactionele outbox koppelt een gecommitte event aan de vereiste
  uitgaande acties. Retries zijn idempotent.

= Configuratie en compatibiliteit

#req[
  *Geen wijziging aan bestaande configuratie.* Geen bestaand veld, grammatica,
  default, bestandspad of interpretatie in de huidige mox-configuratie wordt
  aangepast om deze functies mogelijk te maken.
]

Nieuwe configuratie staat in het afzonderlijke, optionele bestand
`calendar.conf`. Dit bestand omvat zowel agenda als het uitgebreide beheer van
geplande verzending. Afwezigheid van dit bestand betekent:

- bestaande SMTP `FUTURERELEASE` en bestaande webmailfunctionaliteit blijven
  zich gedragen zoals vóór deze uitbreiding;
- de nieuwe calendar-service, CalDAV-endpoints en virtuele `Scheduled`-mailbox
  zijn uitgeschakeld;
- startup en reload vereisen geen migratie van bestaande configbestanden.

Het nieuwe bestand mag uitsluitend instellingen bevatten die niet veilig uit
bestaande account- en listenerconfiguratie kunnen worden afgeleid, bijvoorbeeld
feature-inschakeling, calendar-limieten, schedulingquota en CalDAV-publicatie.
Defaults zijn veilig en backwards compatible. Als `calendar.conf` aanwezig maar
ongeldig is, start mox niet en meldt het de configuratiefout; mox mag nooit met
gedeeltelijk toegepaste nieuwe instellingen starten. Dit verandert of herschrijft
bestaande IMAP/SMTP-configuratie niet.

Bestaande protocollen behouden hun huidige capabilities, commando's, mailboxen,
ACL-gedrag, foutcodes en deliverysemantiek. Nieuwe IMAP- of SMTP-capabilities
worden alleen toegevoegd waar de relevante standaard dat voorschrijft; geen
bestaande capability wordt verwijderd of geherinterpreteerd.

= Gegevensmodel

== ScheduledMessage

Een logisch gepland bericht bevat ten minste:

- stabiele `ScheduledMessageID` en account;
- envelope sender en alle envelope recipients;
- absoluut UTC-tijdstip `ReleaseAt` en `QueuedAt`;
- onderwerp, Message-ID, grootte en verwijzing naar berichtinhoud;
- verwijzingen naar de onderliggende recipient queue-items;
- revisie en status voor optimistic concurrency en racebescherming.

De bestaande queue-inhoud blijft canoniek. De logische groepering mag worden
opgeslagen als expliciete metadata voor nieuwe berichten. Bestaande queue-items
worden conservatief gegroepeerd via account, `BaseID`, Message-ID en
`FutureReleaseRequest`. Een onbetrouwbaar te groeperen bericht blijft
afleverbaar en wordt hoogstens read-only getoond.

== CalendarEvent

Een event bevat ten minste:

- interne ID en globale iCalendar `UID`;
- `SEQUENCE`, creation timestamp, last-modified en ETag/revisie;
- organisator en deelnemers met adres, naam, rol en participation-status;
- titel, beschrijving, locatie, zichtbaarheid en eventstatus;
- begin en einde als datum, lokale tijd met IANA-zone, of UTC-instant;
- `RRULE`, `RDATE`, `EXDATE` en instanties met `RECURRENCE-ID`;
- schedulingmetadata voor ontvangen en verzonden iTIP-acties;
- onbekende geldige iCalendar-properties voor veilige round-tripping.

Concrete momenten worden als UTC-instant vergeleken en geïndexeerd, terwijl de
oorspronkelijke tijdzone en lokale representatie behouden blijven. Een serie
“iedere maandag om 09:00 Europe/Berlin” blijft daardoor 09:00 bij zomer- en
wintertijdwisselingen.

Versie 1 kan een hele reeks of één voorkomen wijzigen en annuleren. De
gebruikersactie “dit en alle volgende” ontbreekt, maar geldige inkomende
`RANGE=THISANDFUTURE`-updates worden voor interoperabiliteit wel verwerkt.

= Stromen

== Plannen, vrijgeven en annuleren

#figure(
  diagram(
    spacing: (7mm, 9mm),
    node-stroke: 0.8pt,
    node-fill: rgb("f7f9fc"),
    node((0, 0), [Client], name: <c>),
    node((2, 0), [Accountservice], name: <s>),
    node((4, 0), [Queue-transactie], name: <q>),
    node((6, 0), [Delivery worker], name: <w>),
    edge(<c>, <s>, "->"),
    edge(<s>, <q>, "->"),
    edge(<q>, <w>, "->"),
    edge(<c>, <s>, "->", bend: 25deg),
    edge(<s>, <q>, "->", bend: -25deg),
  ),
  caption: [Eén queue en conditionele statusovergangen voorkomen gedeeltelijke acties.],
)

Mox valideert account, ontvangers, quota, tijdstip en de maximale bestaande
horizon van 60 dagen. Alle recipient-items worden atomair gegroepeerd. Op
`ReleaseAt` start de normale queuebezorging. Vanaf de eerste deliverypoging is
het logische bericht vergrendeld tegen bewerken of annuleren. Bij een race wint
precies één conditionele statusovergang; de verliezende API-call retourneert de
feitelijke toestand.

`Bewerken` annuleert de geplande queueversie atomair en maakt daarna een concept.
De gebruiker moet dit opnieuw verzenden of plannen. Zo kan de oude versie niet
tegelijk vertrekken. `Nu verzenden` zet vrijgave op nu en activeert de queue.

== Uitgaande uitnodiging

#figure(
  diagram(
    spacing: (7mm, 9mm),
    node-stroke: 0.8pt,
    node-fill: rgb("f7f9fc"),
    node((0, 0), [Webmail/CalDAV], name: <client>),
    node((2, 0), [Domeinservice], name: <service>),
    node((4, 0), [Event + outbox], name: <outbox>),
    node((6, 0), [iTIP/iMIP worker], name: <worker>),
    node((8, 0), [Mailqueue], name: <mq>),
    edge(<client>, <service>, "->"),
    edge(<service>, <outbox>, "->"),
    edge(<outbox>, <worker>, "->"),
    edge(<worker>, <mq>, "->"),
  ),
  caption: [Event en scheduling-outbox worden samen gecommit.],
)

De worker genereert uit een vastgelegde eventrevisie iTIP/iCalendar-berichten.
Interne en externe deelnemers ontvangen gewone e-mail. Tijdelijke
deliveryproblemen volgen het bestaande retrygedrag van de mailqueue. Een retry
van de worker veroorzaakt door een idempotentiesleutel geen tweede logische
update.

== Binnenkomende uitnodiging of reactie

Het originele e-mailbericht blijft normale mail. Tijdens aflevering herkent mox
een calendar-MIME-part en registreert een verwerkingskandidaat. De verwerker
valideert afzender, organizer, attendee, `METHOD`, `UID` en `SEQUENCE`.

Een gebruiker kan accepteren, voorlopig accepteren of weigeren. Daarna werkt mox
de standaardagenda bij en stuurt het een iTIP-reactie. Een deelnemersreactie
wijzigt uitsluitend de status van die deelnemer in het organisator-event.
Verouderde, dubbele, conflicterende of verdachte berichten worden niet blind
toegepast; het mailbericht blijft behouden en webmail toont de toestand.

= Webmailervaring

== Agenda

- Hoofdnavigatie-item `Agenda`, standaard geopend op de huidige week.
- Maand-, week-, dag- en agendaview; mobiel gebruikt primair dag en agenda.
- Klikken of slepen maakt een afspraak met vooraf ingevulde tijden.
- De editor bevat titel, hele dag, begin/einde, tijdzone, locatie, beschrijving,
  herhaling en deelnemers.
- Deelnemers tonen pending, accepted, tentative of declined.
- Bij relevante wijzigingen vraagt mox expliciet of updates moeten worden
  verstuurd.
- Bij een serie vraagt wijzigen/verwijderen: `Alleen deze afspraak` of
  `Hele reeks`.
- Inkomende uitnodigingen bieden in mail en agenda accepteren, voorlopig
  accepteren en weigeren, plus waarschuwingen voor oude, conflicterende of
  verdachte data.
- De UI bevat geen reminders, free/busy of agenda-sharing.

== Geplande mail

- De verzendknop heeft `Nu verzenden` en `Verzending plannen`.
- Na plannen toont webmail lokaal tijdstip én tijdzone.
- `Gepland` toont het aantal wachtende logische berichten.
- Detailacties zijn `Bewerken`, `Herplannen`, `Nu verzenden` en `Annuleren`.
- Na vrijgave verschijnt het gebruikersbericht in `Verzonden`; afzonderlijke
  deliverypogingen worden niet als losse gebruikersberichten gepresenteerd.

= Interfaces

De calendar-JSON-API biedt query binnen een tijdsvenster, get, create, update,
delete en participation-response. Writes vereisen revisie/ETag voor optimistic
concurrency. CalDAV ondersteunt discovery, principal en calendar-home, één
calendar collection, en de noodzakelijke `PROPFIND`, `REPORT`, `GET`, `PUT` en
`DELETE`-semantiek. SSE-account-events verversen open webmailviews.

De scheduled-message-API biedt list, get, cancel, reschedule, release-now en
convert-to-draft. Autorisatie wordt altijd aan het account gekoppeld; beheerders
houden de bestaande queue-instrumenten.

= Veiligheid, grenzen en foutafhandeling

- iCalendar-input heeft limieten voor bytes, componenten, deelnemers,
  uitzonderingen, recurrence-complexiteit en expansiehorizon.
- `ORGANIZER` en `ATTENDEE` worden niet alleen op inhoud vertrouwd. Mox bewaart
  envelope-afzender en authenticatieresultaten en toont afwijkingen. DMARC is
  bewijs, geen absolute autorisatie.
- Alleen de organisator produceert organizerupdates; een deelnemer wijzigt
  uitsluitend de eigen participation-status.
- Oude sequences en exacte duplicaten worden genegeerd. Ambigue conflicten
  blijven zichtbaar en worden niet automatisch samengevoegd.
- Calendar-data en geplande mail vallen onder aparte per-accountlimieten uit de
  nieuwe optionele configuratie.
- Auditlogs bevatten actie, account, identifiers en resultaat, maar geen
  berichtinhoud of gevoelige eventbeschrijvingen.
- Backup, restore en accountverwijdering behandelen calendar store en outbox als
  onderdeel van dezelfde accountlevenscyclus.
- CalDAV gebruikt bestaande mox-authenticatie, TLS-beleid en accountisolatie.

= Migratie en uitrol

De accountdatabase krijgt versiegebonden calendar-tabellen en outboxrecords.
Bestaande accounts krijgen bij activering automatisch één standaardagenda. De
migratie raakt geen bestaande mailrecords of configuratiebestanden.

Gefaseerde oplevering:

1. Intern eventmodel, iCalendar-engine, database en calendar-JSON-API.
2. CalDAV-basis en interoperabiliteit.
3. iTIP/iMIP-uitnodigingen, reacties, updates en annuleringen.
4. Webmail-agenda.
5. ScheduledMessage-service, volledig webmailbeheer en virtuele IMAP-weergave.
6. Hardening, compatibiliteitsmatrix, backup/export en documentatie.

Elke fase heeft een feature gate in de nieuwe afzonderlijke configuratie. Een
rollback schakelt de nieuwe endpoint/UI-laag uit zonder bestaande IMAP/SMTP-data
of -configuratie te herschrijven.

= Teststrategie

- Unit-tests voor tijdzones, DST, hele dagen, recurrence-expansie,
  uitzonderingen, sequences en participation-status.
- Property- en fuzztests voor iCalendar parsing, serialisatie en round-tripping.
- CalDAV-protocoltests voor discovery, properties, reports en CRUD.
- Interoperabiliteitsfixtures van Apple Calendar, Thunderbird, Gmail en Outlook.
- End-to-end uitnodigen, antwoorden, wijzigen en annuleren met interne en
  externe deelnemers.
- Conflicttests voor duplicaten, out-of-order updates, vervalste organizers en
  stale ETags.
- Queue-racetests voor vrijgeven, annuleren, herplannen en convert-to-draft.
- IMAP-tests voor projectie en annulering via `Scheduled`, inclusief één logisch
  bericht met meerdere ontvangers.
- Crash- en herstarttests rond de transactionele outbox.
- Quota- en belastingstests voor grote series, uitzonderingen en deelnemers.
- De volledige bestaande IMAP-, SMTP-, queue-, webmail- en configtestsuite draait
  ongewijzigd als regressiegate. Toegevoegde functies mogen bestaande golden
  protocoltraces en bestaande configuratieparse-resultaten niet veranderen.

= Acceptatiecriteria

- Alle bestaande IMAP- en SMTP-tests slagen zonder gewijzigde verwachtingen.
- Alle bestaande configuratiebestanden worden identiek geaccepteerd en krijgen
  identiek gedrag wanneer de nieuwe configuratie ontbreekt.
- Een event of serie blijft inhoudelijk gelijk na mox–CalDAV-round-trips.
- Gmail- en Outlook-deelnemers ontvangen uitnodigingen en kunnen antwoorden;
  mox verwerkt de status correct.
- Updates, annuleringen en late of dubbele antwoorden corrumperen geen event.
- Een geplande mail wordt nooit vóór `ReleaseAt` vrijgegeven en na succesvolle
  annulering nooit verzonden.
- Een logische actie veroorzaakt hoogstens één schedulingupdate, ook na retries
  of procesherstart.
- Backup en restore behouden events, series, participantstatus en pending
  outboxacties.

= Gebonden implementatiedetails

De volgende keuzes worden in het implementatieplan vastgelegd zonder de
productsemantiek van deze spec te wijzigen:

- de precieze sconf-velden binnen het vastgelegde `calendar.conf`;
- precieze database-indexen en interne pakketgrenzen;
- IMAP UID-strategie voor de virtuele `Scheduled`-projectie;
- ondersteunde CalDAV compliance-features buiten de minimale versie-1-set;
- concrete quota-defaults en maximale recurrence-expansiehorizon.

Deze details zijn bewust geen functionele onzekerheden: iedere keuze moet aan de
compatibiliteitsvoorwaarden, gegevensstromen en acceptatiecriteria hierboven
voldoen.
