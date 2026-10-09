# go-sports-events

```
docker compose up --build   # http://localhost:83
```

## Base de datos

Postgres (servicio `db` en compose, `DATABASE_URL` obligatoria). El server corre dos jobs:

- sync cada `SYNC_EVERY` (2m): guarda los eventos de pelota, juanita y streamed con sus opciones (`events` 1─< `options`); en streamed también `live`/`popular`.
- si hay `JELLYFIN_URL` y `JELLYFIN_API_KEY` (en `.env`, ignorado por git), cuando el sync encuentra un partido nuevo de juanita dispara "Actualizar la guía" en Jellyfin.
- prune cada 1h: borra eventos que empezaron hace más de `PRUNE_AFTER` (4h).

Agenda, playlist, epg y `/api/events` salen de la BD. `/api/stream`, `/api/sports` y scores siguen yendo a upstream (con caché en memoria).

## Endpoints

Todos `GET`.

| Endpoint | Devuelve | Params |
|---|---|---|
| `/api/events` | JSON de eventos | `sport`, `live=1`, `popular=1` |
| `/api/stream` | JSON del stream de un evento | `source`, `id` (requeridos, 422 si faltan) |
| `/api/sports` | JSON de deportes | — |
| `/api/pelota/agenda` | JSON de la agenda de futbollibrehd.me | — |
| `/api/juanita/agenda` | JSON de la agenda de pelisjuanita.com | — |
| `/api/juanita/playlist.m3u` | M3U para Jellyfin, un canal por partido | `full=1` una entrada por opción; `resolve=1` `.m3u8` directos (los tokens expiran) |
| `/api/juanita/epg.xml` | XMLTV, 2h fijas por partido | `full=1` |
| `/api/juanita/tv/playlist.m3u` | M3U de canales 24/7 | `full=1` |
| `/api/tvgarden/playlist.m3u` | M3U de canales abiertos de Argentina (tvgarden/iptv-org), `.m3u8` directos | — |
| `/api/juanita/stream` | 302 al primer `.m3u8` vivo | `u=` repetido o `u[0]=` (400 si falta, 502 si ninguno vive) |
| `/api/juanita/tv/stream` | Igual que `/api/juanita/stream` | Igual |

Los `.../stream` los generan las playlists; no hace falta llamarlos a mano.
