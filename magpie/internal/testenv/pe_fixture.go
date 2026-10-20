package testenv

import (
	"bytes"
	"debug/pe"
	"encoding/binary"
	"testing"
	"unicode/utf16"
)

// PEDLLFixture makes a minimal PE32+ DLL with an RT_VERSION directory and
// VS_FIXEDFILEINFO. It is inert fixture data for parsers, never executable code
// or a library to load; it needs no Windows installation or real runtime DLL.
func PEDLLFixture(t testing.TB, machine uint16, version [4]uint16) []byte {
	t.Helper()
	data := make([]byte, 1024)
	copy(data, "MZ")
	binary.LittleEndian.PutUint32(data[0x3c:], 0x80)
	copy(data[0x80:], "PE\x00\x00")
	header := pe.FileHeader{Machine: machine, NumberOfSections: 1, SizeOfOptionalHeader: 240, Characteristics: pe.IMAGE_FILE_DLL | pe.IMAGE_FILE_EXECUTABLE_IMAGE}
	optional := pe.OptionalHeader64{Magic: 0x20b, ImageBase: 0x180000000, SectionAlignment: 4096, FileAlignment: 512,
		SizeOfImage: 8192, SizeOfHeaders: 512, Subsystem: 3, NumberOfRvaAndSizes: 16}
	optional.DataDirectory[pe.IMAGE_DIRECTORY_ENTRY_RESOURCE] = pe.DataDirectory{VirtualAddress: 0x1000, Size: 512}
	section := pe.SectionHeader32{VirtualSize: 512, VirtualAddress: 0x1000, SizeOfRawData: 512, PointerToRawData: 512, Characteristics: 0x40000040}
	copy(section.Name[:], ".rsrc")
	var headers bytes.Buffer
	for _, value := range []any{header, optional, section} {
		if err := binary.Write(&headers, binary.LittleEndian, value); err != nil {
			t.Fatal(err)
		}
	}
	copy(data[0x84:], headers.Bytes())
	tree := data[512:]
	for _, offset := range []int{0, 24, 48} {
		binary.LittleEndian.PutUint16(tree[offset+14:], 1)
	}
	binary.LittleEndian.PutUint32(tree[16:], 16)
	binary.LittleEndian.PutUint32(tree[20:], 0x80000000|24)
	binary.LittleEndian.PutUint32(tree[40:], 1)
	binary.LittleEndian.PutUint32(tree[44:], 0x80000000|48)
	binary.LittleEndian.PutUint32(tree[64:], 0x409)
	binary.LittleEndian.PutUint32(tree[68:], 72)
	binary.LittleEndian.PutUint32(tree[72:], 0x1000+88)
	binary.LittleEndian.PutUint32(tree[76:], 92)
	value := tree[88:]
	binary.LittleEndian.PutUint16(value, 92)
	binary.LittleEndian.PutUint16(value[2:], 52)
	for i, r := range utf16.Encode([]rune("VS_VERSION_INFO\x00")) {
		binary.LittleEndian.PutUint16(value[6+i*2:], r)
	}
	fixed := value[40:]
	binary.LittleEndian.PutUint32(fixed, 0xfeef04bd)
	binary.LittleEndian.PutUint32(fixed[4:], 0x10000)
	binary.LittleEndian.PutUint32(fixed[8:], uint32(version[0])<<16|uint32(version[1]))
	binary.LittleEndian.PutUint32(fixed[12:], uint32(version[2])<<16|uint32(version[3]))
	binary.LittleEndian.PutUint32(fixed[36:], 2)
	return data
}
