// Package archive turns a Catalyst SupportLogs archive (split, GPG-encrypted,
// gzipped tar) into an extracted directory on disk.
//
// An upload is one or more "SupportLogs.tar.gz.gpg.aa", ".ab", ... parts that
// concatenate into a single symmetric (passphrase) GPG message whose plaintext
// is a gzipped tarball.
package archive

import (
	"archive/tar"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/ProtonMail/go-crypto/openpgp"
	pgperrors "github.com/ProtonMail/go-crypto/openpgp/errors"
	"github.com/ProtonMail/go-crypto/openpgp/packet"
)

// Result reports what an Extract produced, including whether the archive was
// truncated and only partially recovered (the salvage path for incomplete
// uploads).
type Result struct {
	Files           int
	PartialRecovery bool
	Notes           []string
}

// CollectParts resolves an input path to the ordered list of encrypted parts.
//
// The input may be:
//   - a directory containing "*.gpg" / "*.gpg.aa" parts,
//   - a single "...tar.gz.gpg" file, or
//   - one part of a split set ("...gpg.aa"), in which case its siblings are found.
func CollectParts(input string) ([]string, error) {
	fi, err := os.Stat(input)
	if err != nil {
		return nil, err
	}

	var parts []string
	if fi.IsDir() {
		single, _ := filepath.Glob(filepath.Join(input, "*.gpg"))
		split, _ := filepath.Glob(filepath.Join(input, "*.gpg.*"))
		parts = append(parts, single...)
		parts = append(parts, split...)
	} else if isSplitPart(input) {
		// e.g. ".../SupportLogs.tar.gz.gpg.aa" -> glob ".../SupportLogs.tar.gz.gpg.*"
		prefix := strings.TrimSuffix(input, filepath.Ext(input))
		parts, err = filepath.Glob(prefix + ".*")
		if err != nil {
			return nil, err
		}
	} else {
		parts = []string{input}
	}

	if len(parts) == 0 {
		return nil, fmt.Errorf("no .gpg parts found at %s", input)
	}
	sort.Strings(parts) // aa, ab, ac, ...
	return parts, nil
}

// isSplitPart reports whether a path ends in a two-letter split suffix like ".aa".
func isSplitPart(path string) bool {
	ext := filepath.Ext(path) // ".aa"
	if len(ext) != 3 {
		return false
	}
	return ext[1] >= 'a' && ext[1] <= 'z' && ext[2] >= 'a' && ext[2] <= 'z'
}

// partsReader streams the parts in order as one continuous reader.
func partsReader(parts []string) (io.ReadCloser, error) {
	files := make([]*os.File, 0, len(parts))
	for _, p := range parts {
		f, err := os.Open(p)
		if err != nil {
			for _, opened := range files {
				opened.Close()
			}
			return nil, err
		}
		files = append(files, f)
	}
	readers := make([]io.Reader, len(files))
	for i, f := range files {
		readers[i] = f
	}
	return &multiPartReader{Reader: io.MultiReader(readers...), files: files}, nil
}

type multiPartReader struct {
	io.Reader
	files []*os.File
}

func (m *multiPartReader) Close() error {
	var firstErr error
	for _, f := range m.files {
		if err := f.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// decryptedReader concatenates the parts and returns the decrypted plaintext
// (the gzipped tar) as a reader. The closer must be closed by the caller.
func decryptedReader(parts []string, passphrase string) (io.Reader, io.Closer, error) {
	src, err := partsReader(parts)
	if err != nil {
		return nil, nil, err
	}

	var attempted bool
	prompt := func(_ []openpgp.Key, _ bool) ([]byte, error) {
		if attempted {
			return nil, errors.New("incorrect GPG passphrase")
		}
		attempted = true
		return []byte(passphrase), nil
	}

	// Catalyst SupportLogs use legacy symmetric encryption without an MDC, so allow
	// the unauthenticated message (as `gpg --ignore-mdc-error` does). The input is
	// a local file the user supplied.
	config := &packet.Config{InsecureAllowUnauthenticatedMessages: true}
	md, err := openpgp.ReadMessage(src, nil, prompt, config)
	if err != nil {
		src.Close()
		return nil, nil, fmt.Errorf("decrypt: %w", err)
	}
	return md.UnverifiedBody, src, nil
}

// Extract decrypts the parts and unpacks the gzipped tar into outDir.
//
// A truncated upload is salvaged rather than failing outright: every complete
// tar member written before the cut-off is kept, and the result is flagged as a
// partial recovery.
func Extract(parts []string, passphrase, outDir string) (*Result, error) {
	plaintext, closer, err := decryptedReader(parts, passphrase)
	if err != nil {
		return nil, err
	}
	defer closer.Close()

	gz, err := gzip.NewReader(plaintext)
	if err != nil {
		return nil, fmt.Errorf("gunzip: %w", err)
	}
	defer gz.Close()

	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return nil, err
	}

	res := &Result{}
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			if res.salvage(err) {
				break // truncated stream; keep the complete members already written
			}
			return res, fmt.Errorf("untar: %w", err)
		}

		if err := writeEntry(outDir, hdr, tr); err != nil {
			if res.salvage(err) {
				break // truncated mid-file; keep the complete members already written
			}
			return res, err
		}
		if hdr.Typeflag == tar.TypeReg {
			res.Files++
		}
	}
	return res, nil
}

// salvage flags a partial recovery if err is a truncation and at least one
// complete file was already extracted, reporting whether extraction should stop.
func (res *Result) salvage(err error) bool {
	if res.Files == 0 || !isTruncation(err) {
		return false
	}
	res.PartialRecovery = true
	res.Notes = append(res.Notes,
		fmt.Sprintf("archive truncated; extracted %d complete files before the cut-off", res.Files))
	return true
}

func isTruncation(err error) bool {
	if errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF) {
		return true
	}
	// A truncated OpenPGP stream surfaces mid-decryption (after the session key is
	// set) as one of these. The plaintext decrypted so far is still valid, so we
	// treat it as a recoverable cut-off and keep the complete tar members already
	// extracted.
	var (
		dsk pgperrors.DecryptWithSessionKeyError
		se  pgperrors.StructuralError
	)
	return errors.As(err, &dsk) || errors.As(err, &se)
}

// writeEntry writes a single tar member to outDir, guarding against path
// traversal ("zip slip").
func writeEntry(outDir string, hdr *tar.Header, r io.Reader) error {
	target := filepath.Join(outDir, hdr.Name)
	rel, err := filepath.Rel(outDir, target)
	if err != nil || strings.HasPrefix(rel, "..") {
		return fmt.Errorf("unsafe path in archive: %s", hdr.Name)
	}

	switch hdr.Typeflag {
	case tar.TypeDir:
		return os.MkdirAll(target, 0o755)
	case tar.TypeReg:
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		f, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
		if err != nil {
			return err
		}
		if _, err := io.Copy(f, r); err != nil {
			f.Close()
			return err
		}
		return f.Close()
	default:
		// Skip symlinks, devices, etc. — Catalyst archives are plain files/dirs.
		return nil
	}
}
