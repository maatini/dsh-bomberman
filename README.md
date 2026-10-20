# Bomber Grid

Ein vollständiger, serverautoritativer Multiplayer-Bomberman-Clone für den
Browser. Go simuliert das Spiel mit 20 Ticks pro Sekunde; HTML wird per
**HTMX WebSocket Extension** als Out-of-Band-Fragmente gestreamt. Es gibt keine
clientseitige Spiel-Logik und keinen Build-Schritt für das Frontend.

```
┌────────────────────────────── Browser ──────────────────────────────┐
│  HTML + CSS + htmx 2 + htmx-ext-ws + Idiomorph + Web-Audio-SFX      │
│  Keyboard/Touch ──► verstecktes ws-send-Formular ──► JSON-Input     │
│  OOB-HTML-Fragmente ◄── WS ◄── Screen/Chat morphen (15 Hz)         │
└─────────────────────────────────────────────────────────────────────┘
                              │
┌──────────────────────── Go server ───────────────────────────────────┐
│ net/http + gorilla/websocket                                         │
│  internal/ws     Verbindungen, Auth pro Tab, Backpressure/Throttle   │
│  internal/lobby  Räume, Host, Chat, Lobby-Lebenszyklus               │
│  internal/game   autoritative Simulation, Bomben, Power-ups          │
│  internal/web    Routing, html/template, eingebettete Assets         │
└─────────────────────────────────────────────────────────────────────┘
```

## Quickstart mit mise

Voraussetzungen: [mise](https://mise.jdx.dev/) und Netzwerkzugriff für den
ersten Toolchain-Download.

```bash
mise trust            # einmalig im Projektverzeichnis
mise install          # installiert Go 1.27.2 aus mise.toml
mise run dev          # startet http://127.0.0.1:4301
```

`mise run dev` entspricht `go run ./cmd/server`. Der Server liest `BM_ADDR`
(Standard: `127.0.0.1:4301`).

```bash
BM_ADDR=127.0.0.1:9090 mise run dev   # anderer Port
BM_ADDR=0.0.0.0:4301 mise run dev     # im LAN erreichbar (ohne TLS)
BM_MATCH_SECONDS=30 mise run dev      # kürzere Runden, früher Sudden Death
```

Weitere Tasks:

| Task | Zweck |
| --- | --- |
| `mise run dev` | Server starten |
| `mise run build` | Binary nach `bin/bomber-grid` bauen |
| `mise run test` | Unit- und Integrationstests |
| `mise run test:race` | Tests mit Go Race Detector |
| `mise run vet` | `go vet ./...` |
| `mise run fmt` | alle Go-Dateien formatieren |
| `mise run check` | Format-Check, Vet und Tests |
| `mise run clean` | `bin/` entfernen |
| `mise run image` | Container-Image `bomber-grid:dev` bauen |
| `mise run k8s:render` | lokales Kustomize-Overlay rendern |
| `mise run k8s:apply` | lokales Overlay im Cluster anwenden |
| `mise run k8s:delete` | lokale Kustomize-Ressourcen entfernen |

Ohne mise funktioniert auch:

```bash
go run ./cmd/server
```

Es werden keine Node.js-Pakete oder CDN-Verbindungen benötigt: HTMX, die
WebSocket-Extension und Idiomorph liegen versioniert unter
`web/static/vendor/`.

## Lokal spielen

### Solo gegen Bots

1. `mise run dev` starten und <http://127.0.0.1:4301> öffnen.
2. Namen eintragen und **Raum eröffnen**.
3. In der Lobby beim Host auf **+ Bot** klicken (bis zu drei Bots).
4. **Runde starten** – die Bots spielen sofort mit.

Bots laufen vollständig auf dem Server und zählen wie Mitspieler:innen. Sie
weichen Bomben aus, sprengen Ziegel, sammeln Power-ups und greifen an, wenn
sich eine Gelegenheit ergibt.

### Zu zweit spielen

1. `mise run dev` starten und <http://127.0.0.1:4301> öffnen.
2. Namen eintragen und **Raum eröffnen**.
3. In der Lobby **Link kopieren** und den Link in einem zweiten Tab öffnen.
   Alternativ den 5-stelligen Raumcode auf der Startseite eingeben.
4. Im zweiten Tab Namen eintragen und beitreten. Beide Tabs verbinden sich
   automatisch per WebSocket.
5. Sobald zwei Spieler:innen online sind, aktiviert der Host **Runde starten**.

Jeder Tab erhält einen eigenen, zufälligen Sitzungstoken in der URL. Deshalb
funktionieren mehrere Tabs im selben Browser, ohne dass sie dieselbe Spielfigur
teilen. Die Adresszeile (inkl. `?p=…`) ist zugleich der Reconnect-Link: Nach
einem Reload oder kurzen Netzwerkaussetzer verbindet sich der Tab mit derselben
Figur. Der Einladungslink enthält bewusst **keinen** Token.

## Steuerung

| Eingabe | Aktion |
| --- | --- |
| `W` `A` `S` `D` oder Pfeiltasten | Bewegen |
| `Leertaste` oder `E` | Bombe legen |
| `X` | Bombe zünden (nur mit Fernzünder) |
| Touch-D-Pad / BOMBE / ZÜND | Bewegen / Bombe legen / zünden (mobile) |
| `M` oder der ♪-Button | Ton an/aus |
| Enter im Chatfeld | Nachricht senden |

## Sound

Klassische Arcade-Sounds werden vollständig per **Web Audio API** synthetisiert
– es gibt keine Audio-Dateien und keinen zusätzlichen Netzwerk-Request:

- Countdown-Piepser und „Los!“
- Bombenlegen und Explosionen (Rausch- und Subbass-Anteil)
- Treffer, Power-up-Arpeggio
- Sieg-, Niederlage- und Unentschieden-Jingle
- Sirene beim Start von Sudden Death

Der Browser erlaubt Audio erst nach einer Interaktion; der erste Klick oder
Tastendruck entsperrt den Sound. Der ♪-Button im Raumheader bzw. `M` schaltet
ihn stumm. Die Einstellung wird in `localStorage` gespeichert.

## Spielregeln

- 15 × 13 Grid, ungerade Koordinaten; feste Außenmauern und Pfeiler.
- Zerstörbare Ziegel, die beim Explodieren verschwinden und Power-ups fallen
  lassen können.
- Jede:r startet mit **3 Leben**, **1 Bombe**, **Reichweite 2** und normalem
  Tempo.
- Bomben zünden nach 3 s und explodieren in einem Kreuz. Die Explosion stoppt
  an festen Wänden, zerstört Ziegel und löst Kettenreaktionen aus.
- Ein Treffer kostet ein Leben. Wer noch Leben hat, respawnt nach 1,5 s in der
  eigenen Ecke und ist kurz unverwundbar. Wer keine Leben mehr hat, scheidet aus.
- Die Runde endet, sobald nur noch eine Person Leben übrig hat (oder alle
  gleichzeitig ausscheiden → Unentschieden).
- Nach dem Rundenende kann der Host ein Rematch starten.

### Power-ups

| Symbol | Name | Wirkung |
| --- | --- | --- |
| `+` | Extrabombe | ein Bomben-Slot mehr (max. 5) |
| `↔` | Reichweite | ein Feld mehr Explosion (max. 8) |
| `»` | Tempo | schnellere Bewegung |
| `⊣` | Kick | Bomben werden beim Laufen weggestoßen und gleiten weiter |
| `✹` | Fernzünder | Bomben mit `X` (oder erneut `Leertaste`) sofort sprengen |
| `⛨` | Schild | absorbiert genau einen Treffer |

### Sudden Death

Nach **120 Sekunden** ohne Sieger bricht Sudden Death an:

- Der komplette äußere Mauerring zerfällt, es gibt keine sicheren Ecken mehr.
- Bomben regnen vom Himmel. Der Abstand zwischen den Einschlägen wird alle
  10 Sekunden kürzer.
- Damit kann keine Runde mehr durch Campen unentschieden enden.

Die Restzeit bis zum Einsetzen sehen alle Spieler:innen in der Turnierleiste.

### Bots

- Der Host kann in der Lobby bis zu drei Bots hinzufügen und wieder entfernen.
- Ein:e Spieler:in plus ein Bot genügt zum Starten – ideal für eine Solo-Runde.
- Ist die Lobby nur wegen Bots voll, macht ein Bot automatisch Platz, sobald
  ein Mensch beitritt.
- Bots nutzen dieselbe Simulation wie Menschen, kennen aber nur, was auf dem
  Feld sichtbar ist: Sie fliehen vor Bomben, sprengen Ziegel, sammeln Power-ups
  und greifen Gegner:innen im Explosionsradius an.

## Architektur

```
cmd/server/main.go        Flags, HTTP-Server, Graceful Shutdown, Wiring
internal/game/            reine Simulation ohne HTTP/HTML-Wissen
  types.go                Konfiguration, Tiles, Snapshots, Validierung
  board.go                deterministische Board-Generierung
  engine.go               Bewegung, Bomben, Explosionen, Leben, Sieger
  bot.go                  Server-Bots: Gefahrenkarte, Flucht-BFS, Ziele
internal/lobby/           Räume und Lobby-Lebenszyklus
  manager.go              Raum-Codes, Janitor, Maximalanzahl
  room.go                 Host/Join/Start/Rematch, Chat, 20-Hz-Loop
  view.go                 renderfertige, token-spezifische Projektion
internal/ws/hub.go        Upgrade, Auth, Read/Write-Pumps, 15-Hz-Render
internal/web/server.go    Routen, Templates, Security-Header, Redirects
web/templates/            Server-gerenderte Seiten und OOB-Fragmente
web/static/               CSS, Input-/Sound-JS, vendored HTMX/Idiomorph
web/assets.go             //go:embed für ein einziges Binary
Dockerfile                Multi-Stage-Build, statisches Binary, distroless nonroot
deploy/k8s/               Kustomize-Base + lokales/produktives Overlay
```

### Server-Autorität

`internal/game.Engine` ist die einzige Quelle der Wahrheit. Sie kennt keine
WebSockets und keine Templates. Ein `lobby.Room` hält einen Mutex um Engine,
Mitglieder und Chat und ruft `Engine.Tick()` in einem festen 20-Hz-Ticker auf.
Eingaben aus dem WebSocket werden unter demselben Mutex angewendet. Dadurch sind
Bewegung, Bomben, Explosionen und Sieger deterministisch und race-frei.

Jeder Tick erzeugt zunächst nur ein internes Update-Signal. Der WebSocket-Writer
pro Client drosselt das Rendern auf 15 FPS, sodass mehrere Clients und langsame
Verbindungen den Server nicht ausbremsen. Der Renderer liest einen Snapshot
unter dem Room-Lock und führt das Template außerhalb des Game-Loops aus.

### HTMX-Datenfluss

- Die komplette Seite ist normales serverseitiges HTML.
- Der Room-Container besitzt `ws-connect="/ws/room/CODE?p=TOKEN"`.
- Jeder Server-Frame besteht aus zwei OOB-Wurzeln:

  ```html
  <div id="screen" hx-swap-oob="morph:#screen" data-phase="playing">…</div>
  <div id="chat-log" hx-swap-oob="morph:#chat-log">…</div>
  ```

  Idiomorph erhält stabile DOM-Knoten, sodass Figuren per CSS-Transition
  zwischen Zellen gleiten und der Chat nicht flackert.
- Lobby-/Spielinhalte liegen vollständig in `#screen`; das Chat-Formular und die
  versteckte Input-Form liegen außerhalb und behalten Fokus und Listener.
- Bewegung und Bomben kommen aus einem versteckten `<form ws-send>`:
  `{"type":"input","action":"move","dir":"down"}`.
- Chat nutzt ein zweites `ws-send`-Formular:
  `{"type":"chat","message":"…"}`.
- Das eigene JavaScript (`web/static/js/game.js`) ist nur für gehaltene Tasten,
  Touch, Clipboard, Auto-Scroll und Verbindungsstatus zuständig.
- Die HTMX-WebSocket-Extension reconnectet bei abnormalem Schließen
  automatisch. Der Server sendet nach dem Reconnect sofort den aktuellen
  Zustand; die Sitzung bleibt über den URL-Token erhalten.

### Disconnect- und Host-Verhalten

- Jeder Tab ist eine eigene Verbindung; ein Token kann mehrere Verbindungen
  haben, ohne doppelt zu zählen.
- Ein kurzer Reload kostet weder Slot noch Host-Rolle: Der Host wird erst nach
  15 s Offline-Zeit an eine andere verbundene Spieler:in übergeben.
- Ein Spieler, der länger als 45 s offline ist, wird aus der laufenden Runde
  entfernt, damit ein Match nicht an einer „Geisterfigur“ hängen bleibt.
- Ein Verbindungsverlust während der Lobby lässt den Slot bestehen; beim
  Zurückkehren wird derselbe Token wieder verbunden.
- Räume und Tokens liegen ausschließlich im Speicher. Ein Server-Neustart
  beendet alle laufenden Partien.

## WebSocket-Protokoll (Client → Server)

Alle Nachrichten sind kleine JSON-Objekte (max. 2 KiB):

```json
{"type":"input","action":"move","dir":"up"}
{"type":"input","action":"bomb"}
{"type":"input","action":"detonate"}
{"type":"chat","message":"Hallo!"}
```

Server → Client ist HTML, kein JSON. Fehler bei Chat/Start werden als OOB-Toast
oder Formularfehler ausgeliefert; unbekannte Inputs werden ignoriert.

## Tests

```bash
mise run test
mise run test:race
```

Abgedeckt sind unter anderem:

- Board-Generierung und Spawn-Zonen
- Countdown, responsive Bewegung, Bombenlimit, Bombenblockade
- Explosionen, Kettenreaktionen, Ziegel, Power-ups mit Caps
- Kick, Fernzünder und Schild inklusive Grenzfällen
- Sudden Death: Mauerfall, Bombenregen, Restzeit im Snapshot
- Bots: Bewegung, Bomben, vollständige Runde bis zum Sieger
- Leben, Respawn, Unverwundbarkeit, Sieger und Forfeit
- Lobby: Host-Start, Zuschauer, Namen, Chat-Rate-Limit, Host-Grace,
  Disconnect-Forfeit, Bot-Verwaltung
- HTTP-Routen, HTMX-Redirects, Fehlerfragmente, Security-Header
- Echter WebSocket-Roundtrip inklusive Lobby → Start → Input → Chat

## Kubernetes (Kustomize)

Das Deployment liegt unter `deploy/k8s` mit einer Base und den Overlays
`local` und `prod`:

```bash
mise run image                 # docker build -t bomber-grid:dev .
kubectl apply -k deploy/k8s/overlays/local
kubectl -n bomber-grid port-forward service/bomber-grid 4301:4301
```

Danach ist das Spiel unter <http://127.0.0.1:4301> erreichbar. Das lokale
Overlay bietet zusätzlich NodePort `30431`. Das produktive Overlay enthält
einen nginx-Ingress mit langen WebSocket-Timeouts; Image-Registry, Hostname und
TLS-Secret müssen vor dem Apply angepasst werden.

Wichtig: Räume und WebSocket-Zustand liegen im Speicher eines Pods. Das
Deployment läuft deshalb bewusst mit **einer Replica** und `strategy: Recreate`;
Horizontales Skalieren benötigt zuerst einen gemeinsamen Room-State oder
room-aware Routing. Details, Buildx- und Cluster-Beispiele stehen in
[deploy/k8s/README.md](deploy/k8s/README.md).

## Konfiguration

| Variable | Bedeutung | Standard |
| --- | --- | --- |
| `BM_ADDR` | Listen-Adresse | `127.0.0.1:4301` |
| `BM_LOG_LEVEL` | `debug` oder `info` | `info` |
| `BM_MATCH_SECONDS` | Sekunden bis Sudden Death (min. 5) | `120` |
| `-addr` | überschreibt `BM_ADDR` beim Binary | – |

## Bewusste Grenzen

- Keine Persistenz, keine Accounts, kein TLS – gedacht für lokale Runden oder
  ein vertrauenswürdiges LAN. Für öffentliches Hosting einen TLS-Reverse-Proxy
  davorsetzen.
- Kein Matchmaking; Räume werden über Code/Link geteilt.
- Kein Ready-System und keine Hintergrundmusik; die Soundeffekte sind
  synthetisierte Arcade-SFX. Das Spiel ist darauf ausgelegt, leicht erweiterbar
  zu sein: Power-ups in `internal/game`, UI in `web/templates`, Soundereignisse
  in `web/static/js/sound.js`.

Viel Spaß – und denk daran: Weglaufen ist auch eine Strategie. 💣
