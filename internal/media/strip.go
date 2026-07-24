package media

import (
	"bytes"
	"encoding/binary"
	"errors"
)

var ErrNotStrippable = errors.New("media: this format is passed through untouched")

func Strip(mediaType string, body []byte) ([]byte, bool, error) {
	switch mediaType {
	case "image/jpeg":
		out, changed := stripJPEG(body)
		return out, changed, nil
	case "image/png":
		out, changed := stripPNG(body)
		return out, changed, nil
	}
	return body, false, ErrNotStrippable
}

func stripJPEG(body []byte) ([]byte, bool) {
	if len(body) < 4 || body[0] != 0xff || body[1] != 0xd8 {
		return body, false
	}

	out := make([]byte, 0, len(body))
	out = append(out, 0xff, 0xd8)

	i := 2
	changed := false

	for i+3 < len(body) {
		if body[i] != 0xff {
			break
		}

		marker := body[i+1]
		if marker == 0xd8 || marker == 0x01 || (marker >= 0xd0 && marker <= 0xd7) {
			out = append(out, body[i], marker)
			i += 2
			continue
		}
		if marker == 0xda {
			out = append(out, body[i:]...)
			return out, changed
		}

		length := int(binary.BigEndian.Uint16(body[i+2 : i+4]))
		if length < 2 || i+2+length > len(body) {
			break
		}

		if droppedJPEGSegment(marker) {
			changed = true
		} else {
			out = append(out, body[i:i+2+length]...)
		}
		i += 2 + length
	}

	if i < len(body) {
		out = append(out, body[i:]...)
	}
	return out, changed
}

func droppedJPEGSegment(marker byte) bool {
	switch {
	case marker >= 0xe0 && marker <= 0xef:
		return true
	case marker == 0xfe:
		return true
	}
	return false
}

var pngSignature = []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}

func stripPNG(body []byte) ([]byte, bool) {
	if !bytes.HasPrefix(body, pngSignature) {
		return body, false
	}

	out := make([]byte, 0, len(body))
	out = append(out, pngSignature...)

	i := len(pngSignature)
	changed := false

	for i+8 <= len(body) {
		length := int(binary.BigEndian.Uint32(body[i : i+4]))
		if length < 0 || i+12+length > len(body) {
			break
		}

		name := string(body[i+4 : i+8])
		chunk := body[i : i+12+length]

		if droppedPNGChunk(name) {
			changed = true
		} else {
			out = append(out, chunk...)
		}

		i += 12 + length
		if name == "IEND" {
			return out, changed
		}
	}
	return out, changed
}

func droppedPNGChunk(name string) bool {
	switch name {
	case "tEXt", "zTXt", "iTXt", "eXIf", "tIME", "dSIG":
		return true
	}
	return false
}
