package flutter

import (
	"encoding/binary"
	"fmt"
	"os"
)

const (
	machHeaderMagic64 = 0xFEEDFACF
	lcCodeSignature   = 0x1D
	mhDylib           = 0x6
)

// ensureCodeSignatureSpace checks a Mach-O binary and ensures it has an
// LC_CODE_SIGNATURE load command with enough header padding for signing tools.
// If the load command is missing, it adds one pointing to the end of the file
// with a placeholder allocation. This allows third-party signers that can't
// reallocate header space to inject a code signature.
func ensureCodeSignatureSpace(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if len(data) < 32 {
		return fmt.Errorf("file too small for Mach-O")
	}

	magic := binary.LittleEndian.Uint32(data[0:4])
	if magic != machHeaderMagic64 {
		return nil // not a 64-bit Mach-O, skip
	}

	// Mach-O 64 header layout:
	// 0: magic, 4: cputype, 8: cpusubtype, 12: filetype,
	// 16: ncmds, 20: sizeofcmds, 24: flags, 28: reserved
	// Load commands start at offset 32.
	ncmds := binary.LittleEndian.Uint32(data[16:20])
	sizeofcmds := binary.LittleEndian.Uint32(data[20:24])
	headerSize := uint32(32) // mach_header_64 size

	offset := headerSize
	for i := uint32(0); i < ncmds; i++ {
		if offset+8 > headerSize+sizeofcmds {
			break
		}
		cmd := binary.LittleEndian.Uint32(data[offset : offset+4])
		cmdsize := binary.LittleEndian.Uint32(data[offset+4 : offset+8])
		if cmd == lcCodeSignature {
			return nil // already has code signature
		}
		offset += cmdsize
	}

	// Need to add LC_CODE_SIGNATURE load command (16 bytes: cmd, cmdsize, dataoff, datasize)
	lcSize := uint32(16)

	loadCmdsEnd := headerSize + sizeofcmds
	firstSegOffset := findFirstSegmentOffset(data, headerSize, ncmds)
	if firstSegOffset == 0 {
		firstSegOffset = loadCmdsEnd // no padding available
	}

	availablePadding := firstSegOffset - loadCmdsEnd
	if availablePadding < lcSize {
		return fmt.Errorf("not enough header padding (%d bytes free, need %d)", availablePadding, lcSize)
	}

	// Allocate signature space at end of file (16KB is typical minimum)
	sigSize := uint32(16384)
	fileSize := uint32(len(data))

	binary.LittleEndian.PutUint32(data[offset:offset+4], lcCodeSignature)
	binary.LittleEndian.PutUint32(data[offset+4:offset+8], lcSize)
	binary.LittleEndian.PutUint32(data[offset+8:offset+12], fileSize)
	binary.LittleEndian.PutUint32(data[offset+12:offset+16], sigSize)

	binary.LittleEndian.PutUint32(data[16:20], ncmds+1)
	binary.LittleEndian.PutUint32(data[20:24], sizeofcmds+lcSize)

	data = append(data, make([]byte, sigSize)...)

	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, info.Mode())
}

func findFirstSegmentOffset(data []byte, headerSize, ncmds uint32) uint32 {
	const lcSegment64 = 0x19
	offset := headerSize
	for i := uint32(0); i < ncmds; i++ {
		if offset+8 > uint32(len(data)) {
			break
		}
		cmd := binary.LittleEndian.Uint32(data[offset : offset+4])
		cmdsize := binary.LittleEndian.Uint32(data[offset+4 : offset+8])
		if cmd == lcSegment64 && offset+72 <= uint32(len(data)) {
			// LC_SEGMENT_64: offset 48 = fileoff (uint64)
			fileoff := binary.LittleEndian.Uint64(data[offset+48 : offset+56])
			if fileoff > 0 {
				return uint32(fileoff)
			}
		}
		offset += cmdsize
	}
	return 0
}

func isMachODylib(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()

	var header [16]byte
	if _, err := f.Read(header[:]); err != nil {
		return false
	}
	magic := binary.LittleEndian.Uint32(header[0:4])
	if magic != machHeaderMagic64 {
		return false
	}
	filetype := binary.LittleEndian.Uint32(header[12:16])
	return filetype == mhDylib
}
