# Silo Newsletter

Cotygodniowy newsletter e-mail dla serwera [Silo](https://siloserver.org):
nowe filmy i odcinki z plakatami i ocenami (IMDb, Rotten Tomatoes),
osobiste podsumowanie tygodnia każdego użytkownika i najpopularniejsze tytuły
na serwerze. Działa w jednym kontenerze Docker i czyta wyłącznie `/api/v2`
Silo (tylko zapytania GET), bez pluginów.

Wszystko ustawiasz w panelu w przeglądarce: połączenie z Silo, SMTP, treść,
odbiorców i cotygodniowy harmonogram. Jest też podgląd, wysyłka testowa
i historia wysyłek.

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
    ports:
      - "8095:8080"
    environment:
      TZ: Europe/Warsaw
    volumes:
      - /mnt/user/appdata/silo-newsletter:/data
```

1. Przed pierwszym startem: `mkdir -p /mnt/user/appdata/silo-newsletter && chown -R 99:100 /mnt/user/appdata/silo-newsletter`
2. Uruchom kontener i otwórz `http://IP-UNRAIDA:8095`.
3. Wpisz **kod konfiguracji** z logów kontenera (Unraid: ikona kontenera → Logs) i ustaw hasło panelu.
4. Przejdź przez zakładki: Połączenie → E-mail → Treść → Odbiorcy → Harmonogram, sprawdź Podgląd.

Polecenia w terminalu nadal działają (`docker exec silo-newsletter silo-newsletter verify | users | preview <ID> | send-test <ID>`).
Ustawienia ze zmiennych środowiskowych (`SILO_URL`, `SILO_API_KEY`, `SMTP_*`, `FROM_EMAIL`, `TEST_EMAIL`, ...) mają pierwszeństwo przed panelem.

## Bezpieczeństwo panelu

- Hasło jest przechowywane jako bcrypt w `/data/state.json`; pierwsze hasło wymaga jednorazowego kodu z logów.
- Klucz Silo i hasło SMTP nigdy nie są odsyłane do przeglądarki.
- Po 5 błędnych logowaniach panel blokuje próby na minutę.
- Panel nie ma HTTPS: wystawiaj go tylko w sieci domowej lub za reverse proxy z HTTPS.

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
