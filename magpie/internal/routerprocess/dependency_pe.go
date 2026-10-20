package routerprocess

import (
	"debug/pe"
	"encoding/binary"
	"errors"
	"io"
	"unicode/utf16"
)

const maxDependencyResourceBytes = 4 << 20

// Read the resource bytes rather than loading the DLL or using a string search.
// All lengths, RVAs and directory entries are bounded by the verified file.
func validateDependencyPEVersions(file io.ReaderAt, fileSize int64, image *pe.File, want [4]uint16) error {
	header := image.OptionalHeader.(*pe.OptionalHeader64)
	resource := header.DataDirectory[pe.IMAGE_DIRECTORY_ENTRY_RESOURCE]
	if resource.VirtualAddress == 0 || resource.Size == 0 || resource.Size > maxDependencyResourceBytes {
		return errors.New("missing or oversized PE version resource directory")
	}
	tree, err := dependencyPEBytes(file, fileSize, image, resource.VirtualAddress, resource.Size)
	if err != nil {
		return err
	}
	root, err := dependencyResourceEntries(tree, 0)
	if err != nil {
		return err
	}
	found := 0
	for _, kind := range root {
		if kind[0] != 16 { // RT_VERSION, not a named resource.
			continue
		}
		if found != 0 || kind[1]&0x80000000 == 0 {
			return errors.New("ambiguous PE version resource")
		}
		names, err := dependencyResourceEntries(tree, kind[1]&0x7fffffff)
		if err != nil {
			return err
		}
		for _, name := range names {
			if name[1]&0x80000000 == 0 {
				return errors.New("invalid PE version name directory")
			}
			languages, err := dependencyResourceEntries(tree, name[1]&0x7fffffff)
			if err != nil {
				return err
			}
			for _, language := range languages {
				offset := uint64(language[1])
				if language[1]&0x80000000 != 0 || offset+16 > uint64(len(tree)) {
					return errors.New("invalid PE version data entry")
				}
				dataEntry := tree[offset : offset+16]
				rva, size := binary.LittleEndian.Uint32(dataEntry), binary.LittleEndian.Uint32(dataEntry[4:])
				if size == 0 || size > maxDependencyResourceBytes || found >= maxDependencyFiles {
					return errors.New("invalid PE version resource size or count")
				}
				value, err := dependencyPEBytes(file, fileSize, image, rva, size)
				if err != nil {
					return err
				}
				version, err := dependencyFixedVersion(value)
				if err != nil {
					return err
				}
				if version != want {
					return errors.New("PE file version does not match the fixed dependency version")
				}
				found++
			}
		}
	}
	if found == 0 {
		return errors.New("PE has no fixed file version")
	}
	return nil
}

func dependencyResourceEntries(tree []byte, offset uint32) ([][2]uint32, error) {
	start := uint64(offset)
	if start+16 > uint64(len(tree)) {
		return nil, errors.New("PE resource directory is outside the file")
	}
	count := uint64(binary.LittleEndian.Uint16(tree[start+12:])) + uint64(binary.LittleEndian.Uint16(tree[start+14:]))
	if count == 0 || count > 256 || start+16+count*8 > uint64(len(tree)) {
		return nil, errors.New("invalid PE resource directory entry count")
	}
	entries := make([][2]uint32, count)
	for i := range entries {
		entry := tree[start+16+uint64(i)*8:]
		entries[i] = [2]uint32{binary.LittleEndian.Uint32(entry), binary.LittleEndian.Uint32(entry[4:])}
	}
	return entries, nil
}

func dependencyPEBytes(file io.ReaderAt, fileSize int64, image *pe.File, rva, size uint32) ([]byte, error) {
	for _, section := range image.Sections {
		start, address, length := uint64(section.VirtualAddress), uint64(rva), uint64(size)
		if address < start || address-start+length > uint64(section.Size) {
			continue
		}
		offset := uint64(section.Offset) + address - start
		if size == 0 || size > maxDependencyResourceBytes || offset+length > uint64(fileSize) {
			break
		}
		value := make([]byte, int(size))
		if _, err := file.ReadAt(value, int64(offset)); err != nil {
			return nil, err
		}
		return value, nil
	}
	return nil, errors.New("PE resource RVA is outside the file")
}

func dependencyFixedVersion(data []byte) ([4]uint16, error) {
	var version [4]uint16
	if len(data) < 6 {
		return version, errors.New("truncated VS_VERSION_INFO")
	}
	length, valueLength := int(binary.LittleEndian.Uint16(data)), int(binary.LittleEndian.Uint16(data[2:]))
	if length > len(data) || length < 6 || valueLength != 52 || binary.LittleEndian.Uint16(data[4:]) != 0 {
		return version, errors.New("invalid VS_VERSION_INFO header")
	}
	var key []uint16
	offset := 6
	for {
		if offset+2 > length || len(key) > 16 {
			return version, errors.New("invalid VS_VERSION_INFO key")
		}
		r := binary.LittleEndian.Uint16(data[offset:])
		offset += 2
		if r == 0 {
			break
		}
		key = append(key, r)
	}
	offset = (offset + 3) &^ 3
	if string(utf16.Decode(key)) != "VS_VERSION_INFO" || offset+52 > length {
		return version, errors.New("invalid VS_VERSION_INFO value")
	}
	fixed := data[offset : offset+52]
	if binary.LittleEndian.Uint32(fixed) != 0xfeef04bd || binary.LittleEndian.Uint32(fixed[4:]) != 0x00010000 {
		return version, errors.New("invalid VS_FIXEDFILEINFO signature")
	}
	ms, ls := binary.LittleEndian.Uint32(fixed[8:]), binary.LittleEndian.Uint32(fixed[12:])
	return [4]uint16{uint16(ms >> 16), uint16(ms), uint16(ls >> 16), uint16(ls)}, nil
}
