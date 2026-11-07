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

1. Uruchom kontener i otwórz `http://IP-UNRAIDA:8095`. Folder w appdata kontener tworzy i ustawia sam (właściciel `99:100`; inny ustawisz zmiennymi `PUID` i `PGID`).
2. Przejdź przez zakładki: Połączenie → E-mail → Treść → Wygląd → Odbiorcy → Harmonogram, sprawdź Podgląd.

Polecenia w terminalu nadal działają (`docker exec silo-newsletter silo-newsletter verify | users | preview <ID> | send-test <ID>`).
Ustawienia ze zmiennych środowiskowych (`SILO_URL`, `SILO_API_KEY`, `SMTP_*`, `FROM_EMAIL`, `TEST_EMAIL`, ...) mają pierwszeństwo przed panelem.

## Bezpieczeństwo panelu

- Panel nie ma logowania: każdy w sieci, kto otworzy jego adres, może zmienić ustawienia i wysłać newsletter.
- Klucz Silo i hasło SMTP nigdy nie są odsyłane do przeglądarki.
- Nie wystawiaj go do internetu. Jeśli potrzebujesz dostępu z zewnątrz, użyj VPN albo reverse proxy z HTTPS i własnym logowaniem.

## Prywatność

- Każdy dostaje wyłącznie własne statystyki. Odtworzenia z profili dziecięcych
  są pomijane, gdy Silo oznacza profil jako dziecięcy.
- „Najpopularniejsze na serwerze” pokazuje tylko tytuły obejrzane przez co
  najmniej dwa konta i nigdy nie podaje, kto je oglądał.
- Nowości są filtrowane według bibliotek, do których dane konto ma dostęp.
- Klucz API trafia tylko do nagłówka `Authorization`; grafiki (podpisane adresy
  Silo) pobierane są bez klucza z serwera Silo; jedynym wyjątkiem jest logo, jeśli podasz jego adres w zakładce Wygląd.

## Licencja

MIT. Projekt inspirowany [TautWeekly](https://github.com/sparkmoxie/TautWeekly).
