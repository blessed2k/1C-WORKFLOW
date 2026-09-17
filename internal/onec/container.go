// Package onec reads the 1C:Enterprise container format used by .cf/.epf/.cfe
// and by the platform syntax-help files (.hbk). Ported from the documented
// format (onec_dtools / v8unpack).
package onec

import (
	"bytes"
	"compress/flate"
	"encoding/binary"
	"errors"
	"io"
	"strconv"
	"unicode/utf16"
)

// endMarker (0x7FFFFFFF) terminates a block chain and the table of contents.
const endMarker = 0x7fffffff

// blockHeaderSize is the size of a block header: "\r\n%08x %08x %08x \r\n".
const blockHeaderSize = 31

// tocEntrySize is one TOC record: [attrDocOffset u32][dataDocOffset u32][endMarker u32].
const tocEntrySize = 12

// ReadContainer parses a 1C container and returns its entries keyed by name.
// Entry data is deflate-decompressed when it is compressed, otherwise returned
// as-is (e.g. a nested container).
func ReadContainer(data []byte) (map[string][]byte, error) {
	if len(data) < 16 {
		return nil, errors.New("not a 1C container: too short")
	}
	toc, err := readDoc(data, 16)
	if err != nil {
		return nil, err
	}

	entries := make(map[string][]byte)
	for i := 0; i+tocEntrySize <= len(toc); i += tocEntrySize {
		attrOff := binary.LittleEndian.Uint32(toc[i:])
		dataOff := binary.LittleEndian.Uint32(toc[i+4 : i+8])
		if attrOff == endMarker || attrOff == 0 {
			break
		}
		attr, err := readDoc(data, int(attrOff))
		if err != nil {
			continue
		}
		raw, err := readDoc(data, int(dataOff))
		if err != nil {
			continue
		}
		entries[parseName(attr)] = inflateOrRaw(raw)
	}
	return entries, nil
}

// readDoc reads a document that may span a chain of blocks, returning its bytes
// truncated to the document size declared in the first block.
func readDoc(data []byte, offset int) ([]byte, error) {
	var out []byte
	docSize := -1
	for offset != endMarker {
		if offset < 0 || offset+blockHeaderSize > len(data) {
			return nil, errors.New("block header out of range")
		}
		hdr := data[offset : offset+blockHeaderSize]
		ds, err1 := parseHex(hdr[2:10])
		bs, err2 := parseHex(hdr[11:19])
		next, err3 := parseHex(hdr[20:28])
		if err1 != nil || err2 != nil || err3 != nil {
			return nil, errors.New("bad block header")
		}
		if docSize < 0 {
			docSize = int(ds)
		}
		start := offset + blockHeaderSize
		end := start + int(bs)
		if end > len(data) {
			return nil, errors.New("block data out of range")
		}
		out = append(out, data[start:end]...)
		offset = int(next)
	}
	if docSize >= 0 && docSize <= len(out) {
		out = out[:docSize]
	}
	return out, nil
}

func parseHex(b []byte) (uint32, error) {
	v, err := strconv.ParseUint(string(bytes.TrimSpace(b)), 16, 32)
	return uint32(v), err
}

// parseName decodes the entry name from its attribute document: a 20-byte header
// (two FILETIME stamps + a reserved dword) followed by a UTF-16LE name.
func parseName(attr []byte) string {
	if len(attr) <= 20 {
		return ""
	}
	raw := attr[20:]
	u16 := make([]uint16, 0, len(raw)/2)
	for i := 0; i+1 < len(raw); i += 2 {
		c := binary.LittleEndian.Uint16(raw[i:])
		if c == 0 {
			break
		}
		u16 = append(u16, c)
	}
	return string(utf16.Decode(u16))
}

// inflateOrRaw returns the raw-deflate-decompressed content, or the input
// unchanged if it is not compressed.
func inflateOrRaw(raw []byte) []byte {
	r := flate.NewReader(bytes.NewReader(raw))
	defer r.Close()
	out, err := io.ReadAll(r)
	if err != nil || len(out) == 0 {
		return raw
	}
	return out
}
