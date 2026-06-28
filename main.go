// Command exotherm is a local CLI for Catalyst SupportLogs. It opens the
// split, GPG-encrypted, gzipped tar produced by a Catalyst logger, lists the
// sessions inside, and exports a session's telemetry to CSV.
package main

import (
	"database/sql"
	"flag"
	"fmt"
	"os"
	"strconv"
	"text/tabwriter"
	"time"

	"github.com/exotherm/exotherm-go/internal/archive"
	"github.com/exotherm/exotherm-go/internal/catalyst"
	"github.com/exotherm/exotherm-go/internal/export"
)

const passphraseEnv = "EXOTHERM_GPG_PASSPHRASE"

// defaultPassphrase is the fixed passphrase Catalyst SupportLogs are encrypted
// with. It is used when neither --passphrase nor the EXOTHERM_GPG_PASSPHRASE env
// var is set, so the tool works on Catalyst files out of the box.
const defaultPassphrase = "FF59E1881B4ED01C5C2F5F01890AFF25E9764D27"

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "extract":
		err = cmdExtract(os.Args[2:])
	case "sessions":
		err = cmdSessions(os.Args[2:])
	case "export":
		err = cmdExport(os.Args[2:])
	case "-h", "--help", "help":
		usage()
		return
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n", os.Args[1])
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `exotherm - Catalyst telemetry CLI

Usage:
  exotherm extract  <input> -o <dir> [--passphrase P]   Decrypt & unpack an upload
  exotherm sessions <path>          [--passphrase P]     List sessions in a bundle
  exotherm export   <path> --session ID [-o file.csv] [--speed m/s|mph|kph] [--passphrase P]

<input> may be a directory of "*.gpg" parts, a single "...tar.gz.gpg", or one
split part ("...gpg.aa"). <path> may be an already-extracted directory or an
archive (auto-extracted to a temp dir).

The GPG passphrase defaults to the built-in Catalyst passphrase; override it with
--passphrase or the `+passphraseEnv+` env var.
`)
}

// parseArgs parses flags that may appear before or after positional arguments
// (the stdlib flag package stops at the first positional), returning the
// collected positionals.
func parseArgs(fs *flag.FlagSet, args []string) []string {
	var positional []string
	for {
		fs.Parse(args)
		if fs.NArg() == 0 {
			break
		}
		positional = append(positional, fs.Arg(0))
		args = fs.Args()[1:]
	}
	return positional
}

// passphrase resolves the GPG passphrase, preferring --passphrase, then the
// EXOTHERM_GPG_PASSPHRASE env var, and finally the built-in default.
func passphrase(flagVal string) string {
	if flagVal != "" {
		return flagVal
	}
	if v := os.Getenv(passphraseEnv); v != "" {
		return v
	}
	return defaultPassphrase
}

func cmdExtract(args []string) error {
	fs := flag.NewFlagSet("extract", flag.ExitOnError)
	out := fs.String("o", "", "output directory (required)")
	pass := fs.String("passphrase", "", "GPG passphrase")
	pos := parseArgs(fs, args)

	if len(pos) < 1 || *out == "" {
		return fmt.Errorf("usage: exotherm extract <input> -o <dir>")
	}

	parts, err := archive.CollectParts(pos[0])
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "Decrypting and extracting %d part(s)...\n", len(parts))
	res, err := archive.Extract(parts, passphrase(*pass), *out)
	if err != nil {
		return err
	}
	if res.PartialRecovery {
		fmt.Fprintf(os.Stderr, "warning: partial recovery — %v\n", res.Notes)
	}
	fmt.Printf("Extracted %d files to %s\n", res.Files, *out)
	return nil
}

func cmdSessions(args []string) error {
	fs := flag.NewFlagSet("sessions", flag.ExitOnError)
	pass := fs.String("passphrase", "", "GPG passphrase")
	pos := parseArgs(fs, args)
	if len(pos) < 1 {
		return fmt.Errorf("usage: exotherm sessions <path>")
	}

	b, cleanup, err := resolveBundle(pos[0], *pass)
	if err != nil {
		return err
	}
	defer cleanup()
	defer b.Close()

	sessions, err := b.ListSessions()
	if err != nil {
		return err
	}
	if len(sessions) == 0 {
		fmt.Println("No sessions found.")
		return nil
	}

	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tDATE\tTRACK\tCAR\tLAPS\tBEST LAP\tTOP SPEED")
	for _, s := range sessions {
		fmt.Fprintf(tw, "%d\t%s\t%s\t%s\t%s\t%s\t%s\n",
			s.ID,
			time.Unix(s.StartTimeUTCs, 0).Format("2006-01-02 15:04"),
			ns(s.TrackName),
			car(s.Make, s.Model),
			ni(s.NumLaps),
			lapTime(s.FastestLapTimeMs),
			topSpeed(s.MaxSpeedMps),
		)
	}
	return tw.Flush()
}

func cmdExport(args []string) error {
	fs := flag.NewFlagSet("export", flag.ExitOnError)
	sessionID := fs.String("session", "", "session ID to export (required)")
	out := fs.String("o", "", "output CSV file (default: stdout)")
	speed := fs.String("speed", "m/s", "speed unit: m/s, mph, or kph")
	pass := fs.String("passphrase", "", "GPG passphrase")
	pos := parseArgs(fs, args)

	if len(pos) < 1 || *sessionID == "" {
		return fmt.Errorf("usage: exotherm export <path> --session ID [-o file.csv]")
	}
	unit, err := export.ParseSpeedUnit(*speed)
	if err != nil {
		return err
	}

	b, cleanup, err := resolveBundle(pos[0], *pass)
	if err != nil {
		return err
	}
	defer cleanup()
	defer b.Close()

	if _, err := b.GetSession(*sessionID); err != nil {
		return err
	}
	laps, err := b.Laps(*sessionID)
	if err != nil {
		return err
	}
	if len(laps) == 0 {
		return fmt.Errorf("session %s has no laps", *sessionID)
	}

	w := os.Stdout
	if *out != "" {
		f, err := os.Create(*out)
		if err != nil {
			return err
		}
		defer f.Close()
		w = f
	}

	stats, err := export.SessionCSV(w, b, laps, unit)
	if err != nil {
		return err
	}
	dest := "stdout"
	if *out != "" {
		dest = *out
	}
	fmt.Fprintf(os.Stderr, "Wrote %d rows from %d laps to %s", stats.Rows, stats.Laps, dest)
	if stats.Skipped > 0 {
		fmt.Fprintf(os.Stderr, " (%d laps skipped — no telemetry)", stats.Skipped)
	}
	fmt.Fprintln(os.Stderr)
	return nil
}

// resolveBundle opens path as an extracted bundle, or auto-extracts an archive
// to a temp dir. The returned cleanup removes any temp dir.
func resolveBundle(path, passFlag string) (*catalyst.Bundle, func(), error) {
	noop := func() {}

	fi, err := os.Stat(path)
	if err != nil {
		return nil, noop, err
	}

	// An extracted directory already containing catalyst.db.
	if fi.IsDir() {
		if b, err := catalyst.Open(path); err == nil {
			return b, noop, nil
		}
		// Otherwise fall through: maybe a directory of .gpg parts.
	}

	// Treat as an archive: decrypt + extract into a temp dir.
	parts, err := archive.CollectParts(path)
	if err != nil {
		return nil, noop, err
	}
	tmp, err := os.MkdirTemp("", "exotherm-")
	if err != nil {
		return nil, noop, err
	}
	cleanup := func() { os.RemoveAll(tmp) }

	fmt.Fprintf(os.Stderr, "Decrypting and extracting %d part(s) to a temp dir...\n", len(parts))
	res, err := archive.Extract(parts, passphrase(passFlag), tmp)
	if err != nil {
		cleanup()
		return nil, noop, err
	}
	if res.PartialRecovery {
		fmt.Fprintf(os.Stderr, "warning: partial recovery — %v\n", res.Notes)
	}
	b, err := catalyst.Open(tmp)
	if err != nil {
		cleanup()
		return nil, noop, err
	}
	return b, cleanup, nil
}

// --- small formatting helpers for the sessions table ---

func ns(v sql.NullString) string {
	if v.Valid && v.String != "" {
		return v.String
	}
	return "-"
}

func ni(v sql.NullInt64) string {
	if v.Valid {
		return strconv.FormatInt(v.Int64, 10)
	}
	return "-"
}

func car(mk, model sql.NullString) string {
	switch {
	case mk.Valid && model.Valid:
		return mk.String + " " + model.String
	case mk.Valid:
		return mk.String
	case model.Valid:
		return model.String
	default:
		return "-"
	}
}

// lapTime renders a millisecond lap time as m:ss.mmm.
func lapTime(v sql.NullInt64) string {
	if !v.Valid || v.Int64 <= 0 {
		return "-"
	}
	ms := v.Int64
	return fmt.Sprintf("%d:%06.3f", ms/60000, float64(ms%60000)/1000.0)
}

// topSpeed renders a m/s max speed in km/h.
func topSpeed(v sql.NullFloat64) string {
	if !v.Valid {
		return "-"
	}
	return fmt.Sprintf("%.1f km/h", v.Float64*3.6)
}
