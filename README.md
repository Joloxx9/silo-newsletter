# Silo Newsletter

Cotygodniowy newsletter e-mail dla serwera [Silo](https://siloserver.org):
nowe filmy i odcinki z plakatami i ocenami (IMDb, Rotten Tomatoes),
osobiste podsumowanie tygodnia każdego użytkownika i najpopularniejsze tytuły
na serwerze. Działa w jednym kontenerze Docker i czyta wyłącznie `/api/v2`
Silo (tylko zapytania GET), bez pluginów.

> Etap 1: newsletter z linii poleceń (podgląd i wysyłka testowa).
> Harmonogram, wysyłka do wszystkich i panel w przeglądarce są w planach.

## Wymagania

- Silo z kluczem API **administratora bez ograniczeń uprawnień** (bez scope).
- Konto SMTP (np. Gmail z hasłem aplikacji).

## Uruchomienie (Unraid / Docker Compose)

```yaml
services:
  silo-newsletter:
    image: ghcr.io/joloxx9/silo-newsletter:latest
    container_name: silo-newsletter
    restart: unless-stopped
    environment:
      TZ: Europe/Warsaw
    volumes:
      - /mnt/user/appdata/silo-newsletter:/data
```

Skopiuj `config.example.json` do `/mnt/user/appdata/silo-newsletter/config.json`
i uzupełnij. Potem:

```bash
docker exec silo-newsletter silo-newsletter verify          # test połączenia
docker exec silo-newsletter silo-newsletter users           # lista kont i ich ID
docker exec silo-newsletter silo-newsletter preview 1       # podgląd HTML w /data/output
docker exec silo-newsletter silo-newsletter send-test 1     # wysyłka na TestEmail
```

Ustawienia można też podać zmiennymi środowiskowymi: `SILO_URL`, `SILO_API_KEY`,
`SMTP_HOST`, `SMTP_PORT`, `SMTP_USERNAME`, `SMTP_PASSWORD`, `SMTP_SECURITY`,
`FROM_EMAIL`, `FROM_NAME`, `TEST_EMAIL`, `REPLY_TO_EMAIL`, `SERVER_NAME`,
`NEWSLETTER_TZ`.

## Prywatność

- Każdy dostaje wyłącznie własne statystyki. Odtworzenia z profili dziecięcych
  są pomijane, gdy Silo oznacza profil jako dziecięcy.
- „Najpopularniejsze na serwerze” pokazuje tylko tytuły obejrzane przez co
  najmniej dwa konta i nigdy nie podaje, kto je oglądał.
- Nowości są filtrowane według bibliotek, do których dane konto ma dostęp.
- Klucz API trafia tylko do nagłówka `Authorization`; grafiki (podpisane adresy
  Silo) pobierane są bez klucza i wyłącznie z serwera Silo.

## Licencja

MIT. Projekt inspirowany [TautWeekly](https://github.com/sparkmoxie/TautWeekly).
