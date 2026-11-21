package aseprite

import "encoding/binary"

// mulInt64 returns a*b and false when the multiplication overflows int64.
func mulInt64(a, b int64) (int64, bool) {
	if a == 0 || b == 0 {
		return 0, true
	}
	p := a * b
	if p/b != a {
		return 0, false
	}
	return p, true
}

// mulInt returns a*b and false when the multiplication overflows int.
func mulInt(a, b int) (int, bool) {
	p, ok := mulInt64(int64(a), int64(b))
	return int(p), ok && p == int64(int(p))
}

// addInt returns a+b and false when the addition overflows int.
func addInt(a, b int) (int, bool) {
	s := int64(a) + int64(b)
	if s != int64(int(s)) {
		return 0, false
	}
	return int(s), true
}

// at reports whether raw[i:i+n] is in range.
func at(raw []byte, i, n int) bool {
	return i >= 0 && n >= 0 && i <= len(raw)-n
}

func u16at(raw []byte, i int) (uint16, bool) {
	if !at(raw, i, 2) {
		return 0, false
	}
	return binary.LittleEndian.Uint16(raw[i:]), true
}

func u32at(raw []byte, i int) (uint32, bool) {
	if !at(raw, i, 4) {
		return 0, false
	}
	return binary.LittleEndian.Uint32(raw[i:]), true
}

// sub returns raw[i:j] with an ok flag instead of panicking on bad indices.
func sub(raw []byte, i, j int) ([]byte, bool) {
	if i < 0 || j < i || j > len(raw) {
		return nil, false
	}
	return raw[i:j], true
}
