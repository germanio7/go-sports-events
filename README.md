# go-sports-events

```
docker compose up --build   # http://localhost:8080 (PORT para cambiarlo)
```

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
| `/api/juanita/stream` | 302 al primer `.m3u8` vivo | `u=` repetido o `u[0]=` (400 si falta, 502 si ninguno vive) |
| `/api/juanita/tv/stream` | Igual que `/api/juanita/stream` | Igual |

Los `.../stream` los generan las playlists; no hace falta llamarlos a mano.
