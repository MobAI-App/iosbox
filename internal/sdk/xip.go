package sdk

import (
	"bytes"
	"compress/bzip2"
	"compress/gzip"
	"compress/zlib"
	"encoding/binary"
	"encoding/xml"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"time"

	"github.com/klauspost/compress/zstd"
	"github.com/ulikunitz/xz"
)

const xarMagic = 0x78617221

// xarRawHeader is the on-disk XAR header (big-endian).
type xarRawHeader struct {
	Magic        uint32
	HeaderSize   uint16
	Version      uint16
	TocLenComp   uint64
	TocLenUncomp uint64
	CksumAlg     uint32
}

type tocFile struct {
	Name  string    `xml:"name"`
	Data  tocData   `xml:"data"`
	Files []tocFile `xml:"file"`
}

type tocData struct {
	Offset   int64 `xml:"offset"`
	Length   int64 `xml:"length"`
	Encoding struct {
		Style string `xml:"style,attr"`
	} `xml:"encoding"`
}

type tocXML struct {
	Files []tocFile `xml:"toc>file"`
}

// ExtractXIP extracts a Xcode.xip archive to destDir and returns the path
// to Xcode.app. Requires cpio to be available on PATH.
// If Xcode.app is already present in destDir, extraction is skipped.
func ExtractXIP(xipPath, destDir string) (string, error) {
	entries, _ := os.ReadDir(destDir)
	for _, e := range entries {
		if e.IsDir() && filepath.Ext(e.Name()) == ".app" {
			appPath := filepath.Join(destDir, e.Name())
			fmt.Printf("  already extracted: %s\n", appPath)
			return appPath, nil
		}
	}

	f, err := os.Open(xipPath)
	if err != nil {
		return "", fmt.Errorf("open xip: %w", err)
	}
	defer f.Close()

	var hdr xarRawHeader
	if err := binary.Read(f, binary.BigEndian, &hdr); err != nil {
		return "", fmt.Errorf("read xar header: %w", err)
	}
	if hdr.Magic != xarMagic {
		return "", fmt.Errorf("not a XAR/XIP file (magic 0x%08x)", hdr.Magic)
	}

	if _, err := f.Seek(int64(hdr.HeaderSize), io.SeekStart); err != nil {
		return "", err
	}

	tocComp := make([]byte, hdr.TocLenComp)
	if _, err := io.ReadFull(f, tocComp); err != nil {
		return "", fmt.Errorf("read toc: %w", err)
	}
	zr, err := zlib.NewReader(bytes.NewReader(tocComp))
	if err != nil {
		return "", fmt.Errorf("decompress toc: %w", err)
	}
	tocBytes, _ := io.ReadAll(zr)
	zr.Close()

	var toc tocXML
	if err := xml.Unmarshal(tocBytes, &toc); err != nil {
		return "", fmt.Errorf("parse toc xml: %w", err)
	}
	if os.Getenv("IOSBOX_DEBUG_XIP") != "" {
		fmt.Fprintf(os.Stderr, "[xar] headerSize=%d tocLenComp=%d heapBase=%d\n",
			hdr.HeaderSize, hdr.TocLenComp, int64(hdr.HeaderSize)+int64(hdr.TocLenComp))
		fmt.Fprintf(os.Stderr, "[xar] TOC entries:\n")
		printTOCEntries(toc.Files, "  ")
	}

	heapBase := int64(hdr.HeaderSize) + int64(hdr.TocLenComp)

	contentEntry := findTOCFile(toc.Files, "Content")
	if contentEntry == nil {
		return "", fmt.Errorf("Content not found in XIP archive")
	}

	if _, err := f.Seek(heapBase+contentEntry.Data.Offset, io.SeekStart); err != nil {
		return "", err
	}
	raw := io.LimitReader(f, contentEntry.Data.Length)

	pr := &progressReader{r: raw, total: contentEntry.Data.Length}
	stopProgress := pr.start()
	defer stopProgress()

	decoded, err := decodeXAREntry(pr, contentEntry.Data.Encoding.Style)
	if err != nil {
		return "", fmt.Errorf("decode xar entry: %w", err)
	}

	peek := make([]byte, 6)
	n, _ := io.ReadFull(decoded, peek)
	decoded = io.MultiReader(bytes.NewReader(peek[:n]), decoded)

	var cpioStream io.Reader
	switch {
	case n >= 4 && string(peek[:4]) == "pbzx":
		// Modern Xcode: pbzx-framed XZ chunks → CPIO
		cpioStream, err = decodePbzx(decoded)
		if err != nil {
			return "", fmt.Errorf("decode pbzx: %w", err)
		}
	case n >= 6 && (string(peek[:6]) == "070701" || string(peek[:6]) == "070707"):
		// Raw CPIO (unlikely but handle it)
		cpioStream = decoded
	default:
		// Try XZ directly
		cpioStream, err = xz.NewReader(decoded)
		if err != nil {
			return "", fmt.Errorf("unknown Content format (peek: %x)", peek[:n])
		}
	}

	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return "", err
	}
	fmt.Printf("  extracting to %s ...\n", destDir)
	cpioCmd := exec.Command("cpio", "-idm", "--quiet", "--no-absolute-filenames")
	cpioCmd.Stdin = cpioStream
	cpioCmd.Dir = destDir
	cpioCmd.Stderr = os.Stderr
	if err := cpioCmd.Run(); err != nil {
		return "", fmt.Errorf("cpio extraction: %w", err)
	}

	entries, _ = os.ReadDir(destDir)
	for _, e := range entries {
		if e.IsDir() && filepath.Ext(e.Name()) == ".app" {
			return filepath.Join(destDir, e.Name()), nil
		}
	}
	return "", fmt.Errorf("Xcode.app not found in %s after extraction", destDir)
}

// decodeXAREntry applies the XAR-level encoding declared in the TOC.
// For XIP files the Content entry is typically raw (octet-stream).
func decodeXAREntry(r io.Reader, style string) (io.Reader, error) {
	switch style {
	case "application/x-xz":
		return xz.NewReader(r)
	case "application/x-gzip":
		return gzip.NewReader(r)
	case "application/x-bzip2":
		return bzip2.NewReader(r), nil
	default:
		return r, nil
	}
}

// decodePbzx decodes Apple's pbzx-framed archive into a CPIO stream.
//
// Legacy format (Xcode ≤ 15):
//
//	"pbzx" magic (4 bytes)
//	Repeated blocks: flags uint64 BE (bit 63 = compressed) | compSize uint64 BE | data
//
// Modern format (Xcode 16+):
//
//	"pbzx" magic (4 bytes)
//	streamHeader uint64 BE (skipped)
//	Repeated blocks: uncompSize uint64 BE | compSize uint64 BE | data (XZ or zstd)
func decodePbzx(r io.Reader) (io.Reader, error) {
	var magic [4]byte
	if _, err := io.ReadFull(r, magic[:]); err != nil {
		return nil, err
	}

	// Read first 8 bytes to detect format:
	// - Legacy: flags with bit 63 set for compressed blocks
	// - Modern: stream header (no bit 63), followed by per-block (uncompSize|compSize)
	var first8 [8]byte
	if _, err := io.ReadFull(r, first8[:]); err != nil {
		return nil, fmt.Errorf("pbzx: read stream header: %w", err)
	}
	word0 := binary.BigEndian.Uint64(first8[:])
	modern := word0&0x8000000000000000 == 0

	debug := os.Getenv("IOSBOX_DEBUG_XIP") != ""
	if debug {
		fmt.Fprintf(os.Stderr, "[pbzx] word0=0x%016x modern=%v\n", word0, modern)
	}

	pr, pw := io.Pipe()
	go func() {
		defer pw.Close()

		var zstdDec *zstd.Decoder

		firstBlockFlags := uint64(0)
		hasFirstFlags := false
		if !modern {
			firstBlockFlags = word0
			hasFirstFlags = true
		}

		blockN := 0
		for {
			var flags, compSize uint64

			if hasFirstFlags {
				flags = firstBlockFlags
				hasFirstFlags = false
				var b [8]byte
				if _, err := io.ReadFull(r, b[:]); err != nil {
					if debug {
						fmt.Fprintf(os.Stderr, "[pbzx] block %d: EOF reading compSize (first block legacy): %v\n", blockN, err)
					}
					return
				}
				compSize = binary.BigEndian.Uint64(b[:])
			} else {
				var hdr [16]byte
				if _, err := io.ReadFull(r, hdr[:]); err != nil {
					if debug {
						fmt.Fprintf(os.Stderr, "[pbzx] block %d: EOF reading header: %v\n", blockN, err)
					}
					return // EOF
				}
				flags = binary.BigEndian.Uint64(hdr[:8])
				compSize = binary.BigEndian.Uint64(hdr[8:])
			}

			if debug {
				fmt.Fprintf(os.Stderr, "[pbzx] block %d: flags=0x%016x compSize=%d\n", blockN, flags, compSize)
			}
			blockN++

			if compSize > 1<<31 {
				pw.CloseWithError(fmt.Errorf("pbzx: implausible compSize %d", compSize))
				return
			}

			chunk := make([]byte, compSize)
			if _, err := io.ReadFull(r, chunk); err != nil {
				pw.CloseWithError(fmt.Errorf("pbzx: read chunk: %w", err))
				return
			}

			isCompressed := !modern && flags&0x8000000000000000 != 0 ||
				modern && compSize < flags
			if !isCompressed {
				if _, err := pw.Write(chunk); err != nil {
					return
				}
				continue
			}

			var decompressed io.Reader
			if len(chunk) >= 4 && chunk[0] == 0x28 && chunk[1] == 0xb5 && chunk[2] == 0x2f && chunk[3] == 0xfd {
				if zstdDec == nil {
					zstdDec, _ = zstd.NewReader(nil)
				}
				decoded, err := zstdDec.DecodeAll(chunk, nil)
				if err != nil {
					pw.CloseWithError(fmt.Errorf("pbzx zstd: %w", err))
					return
				}
				if _, err := pw.Write(decoded); err != nil {
					return
				}
				continue
			} else {
				xzr, err := xz.NewReader(bytes.NewReader(chunk))
				if err != nil {
					pw.CloseWithError(fmt.Errorf("pbzx xz: %w", err))
					return
				}
				decompressed = xzr
			}
			if _, err := io.Copy(pw, decompressed); err != nil {
				pw.CloseWithError(err)
				return
			}
		}
	}()
	return pr, nil
}

func printTOCEntries(files []tocFile, indent string) {
	for _, f := range files {
		fmt.Fprintf(os.Stderr, "%s%s  offset=%d length=%d encoding=%q\n",
			indent, f.Name, f.Data.Offset, f.Data.Length, f.Data.Encoding.Style)
		printTOCEntries(f.Files, indent+"  ")
	}
}

type progressReader struct {
	r     io.Reader
	total int64
	read  atomic.Int64
}

func (p *progressReader) Read(b []byte) (int, error) {
	n, err := p.r.Read(b)
	p.read.Add(int64(n))
	return n, err
}

func (p *progressReader) start() func() {
	done := make(chan struct{})
	go func() {
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		var lastPrinted int64
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				read := p.read.Load()
				if read-lastPrinted < 100e6 {
					continue
				}
				lastPrinted = read
				if p.total > 0 {
					pct := float64(read) / float64(p.total) * 100
					fmt.Printf("  extracting: %.1f / %.1f GB (%.0f%%)\n",
						float64(read)/1e9, float64(p.total)/1e9, pct)
				} else {
					fmt.Printf("  extracting: %.1f GB\n", float64(read)/1e9)
				}
			}
		}
	}()
	return func() { close(done) }
}

func findTOCFile(files []tocFile, name string) *tocFile {
	for i := range files {
		if files[i].Name == name {
			return &files[i]
		}
		if found := findTOCFile(files[i].Files, name); found != nil {
			return found
		}
	}
	return nil
}
