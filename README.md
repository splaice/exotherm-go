# exotherm-go

A small command-line tool for reading [Garmin Catalyst v1](https://www.garmin.com/en-US/p/690726/) data
logger SupportLogs on your own machine. It can:

- **extract** the GPG-encrypted, gzipped tar archive a Catalyst logger produces,
- **list** the sessions inside it, and
- **export** a session's telemetry to CSV.

It's a single self-contained binary with no system dependencies.

Unsure how to run this? Use the hosted version of [Exotherm](https://exotherm.dev).

## How it works

A Catalyst SupportLogs archive (`SupportLogs.tar.gz.gpg`, split into `.aa`, `.ab`,
… parts as necessary) is a gzipped tar that has been symmetrically GPG-encrypted.

To read them Exotherm:

1. concatenates the split parts in order,
2. decrypts the GPG message,
3. unpacks the gzipped tar,
4. reads `catalyst.db` (SQLite) for session and lap metadata,
5. decodes each lap's binary telemetry file, and
6. writes the result as CSV.

## Build

```bash
go build -o bin/exotherm .
```

The only dependencies are [`modernc.org/sqlite`](https://pkg.go.dev/modernc.org/sqlite)
and [`ProtonMail/go-crypto`](https://pkg.go.dev/github.com/ProtonMail/go-crypto),
both pure Go (no cgo). The result is a static binary you can cross-compile for
macOS, Windows, or Linux with `GOOS`/`GOARCH`.

## Usage

Catalyst archives are encrypted with a fixed passphrase, which exotherm uses by
default — so for normal Catalyst files no setup is needed. Override it with
`--passphrase` or the `EXOTHERM_GPG_PASSPHRASE` environment variable if your
files use a different one.

### Extract (decrypt + unpack)

```bash
exotherm extract ./upload -o ./extracted
```

The input may be a directory of `*.gpg` parts, a single `...tar.gz.gpg` file, or
one split part (`...gpg.aa`) whose siblings are found automatically. Truncated or
incomplete uploads are salvaged: every complete file before the cut-off is kept
and a partial-recovery warning is printed.

### List sessions

```bash
exotherm sessions ./extracted
```

```
ID   DATE              TRACK                           CAR          LAPS  BEST LAP  TOP SPEED
145  2025-09-25 13:39  Virginia International Raceway   LOTUS EVORA  5     2:11.944  237.9 km/h
...
```

The path may be an already-extracted directory **or** an archive — in the latter
case it's auto-extracted to a temporary directory first. For large uploads, run
`extract` once and point the other commands at the extracted directory.

### Export a session to CSV

```bash
exotherm export ./extracted --session 145 -o session145.csv
exotherm export ./extracted --session 145 --speed kph -o session145.csv
exotherm export ./extracted --session 145          # writes to stdout
```

`--speed` sets the speed column unit: `m/s` (default), `mph`, or `kph`. Every lap
in the session (including out/in laps) is written as one row per telemetry sample.

## Demo

`testdata/demo/` holds a small (~130 KB) self-contained example archive
(`SupportLogs.tar.gz.gpg.aa`) so you can try the tool without your own data. It
contains two sessions (one at NCCAR, one at Virginia International Raceway) with
their lap telemetry decimated to keep the file small. It's encrypted with the
default passphrase, so:

```bash
exotherm sessions testdata/demo
exotherm export   testdata/demo --session 124 -o demo.csv
```

## Tests

```bash
go test ./...
```

The tests run against the demo fixture: the full decrypt → list → export
pipeline, truncation recovery, and the telemetry parser (synthetic records that
pin the binary layout, plus parsing of every real lap file in the fixture).

## Project layout

```
main.go                        command dispatch and output formatting
internal/archive/archive.go    collect parts, GPG decrypt, gunzip + untar (with salvage)
internal/catalyst/catalyst.go  open catalyst.db, list/get sessions, resolve lap files
internal/catalyst/telemetry.go decode the binary lap records
internal/export/csv.go         flatten laps + telemetry into CSV
testdata/demo/                 small two-session example archive (one split part)
```

## Format notes

- The GPG layer uses legacy symmetric encryption without an MDC, so exotherm
  decrypts these unauthenticated messages on purpose.
- Each binary lap file is a 20-byte header (with an `int32` record count at
  offset 4) followed by fixed 76-byte little-endian records. See `telemetry.go`
  for the field offsets.
- Speed is stored in MPH on the device and converted on export.
