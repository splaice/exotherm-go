package pack

import (
	"encoding/binary"
	"io"
	"os"
)

// videoState is what the card has for a session's raw recording.
type videoState string

const (
	videoOK      videoState = "ok"
	videoMissing videoState = "missing"
	videoBroken  videoState = "broken"
)

// classifyVideo reports whether path is a finished MP4. Catalyst leaves a
// recording without a moov box when the file was cut off, and those will not play.
func classifyVideo(path string) videoState {
	if path == "" {
		return videoMissing
	}
	ok, err := mp4HasMoov(path)
	if err != nil {
		if os.IsNotExist(err) {
			return videoMissing
		}
		return videoBroken
	}
	if !ok {
		return videoBroken
	}
	return videoOK
}

// mp4HasMoov walks the top-level boxes. A file that ends mid-box, or that
// never contains moov, cannot be played.
func mp4HasMoov(path string) (bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return false, err
	}
	var off int64
	found := false
	for off+8 <= st.Size() {
		var hdr [8]byte
		if _, err := f.ReadAt(hdr[:], off); err != nil {
			return false, err
		}
		size := int64(binary.BigEndian.Uint32(hdr[0:4]))
		typ := string(hdr[4:8])
		header := int64(8)
		if size == 1 {
			var ext [8]byte
			if _, err := f.ReadAt(ext[:], off+8); err != nil {
				return false, err
			}
			size = int64(binary.BigEndian.Uint64(ext[:]))
			header = 16
		} else if size == 0 {
			size = st.Size() - off
		}
		if size < header || off+size > st.Size() {
			return false, nil
		}
		if typ == "moov" {
			found = true
		}
		off += size
	}
	return found, nil
}

func copyFile(dst, src string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(out, in)
	closeErr := out.Close()
	if copyErr != nil {
		return copyErr
	}
	return closeErr
}
