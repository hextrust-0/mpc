//
// parse.go
//
// Copyright (c) 2021 Markku Rossi
//
// All rights reserved.
//

package types

import (
	"fmt"
	"regexp"
	"strconv"
)

var (
	reArr   = regexp.MustCompilePOSIX(`^\[([[:digit:]]+)\](.+)$`)
	reSized = regexp.MustCompilePOSIX(`^([[:^digit:]]+)([[:digit:]]+)$`)
)

// Parse parses type definition and returns its type information.
func Parse(val string) (info Info, err error) {
	var ival int

	switch val {
	case "b", "bool":
		info.Type = TBool
		info.Bits = 1
		info.MinBits = info.Bits
		return

	case "byte":
		info.Type = TUint
		info.Bits = 8
		info.MinBits = info.Bits
		return
	}

	m := reSized.FindStringSubmatch(val)
	if m != nil {
		switch m[1] {
		case "i", "int":
			info.Type = TInt

		case "u", "uint":
			info.Type = TUint

		case "s", "string":
			info.Type = TString

		default:
			return info, fmt.Errorf("unknown type: %s", val)
		}
		ival, err = strconv.Atoi(m[2])
		if err != nil {
			return
		}
		info.Bits = Size(ival)
		info.MinBits = info.Bits
		return
	}

	m = reArr.FindStringSubmatch(val)
	if m == nil {
		return info, fmt.Errorf("unknown type: %s", val)
	}
	var elType Info
	elType, err = Parse(m[2])
	if err != nil {
		return
	}
	ival, err = strconv.Atoi(m[1])
	if err != nil {
		return
	}

	info.Type = TArray
	info.Bits = Size(ival) * elType.Bits
	info.MinBits = info.Bits
	info.ElementType = &elType
	info.ArraySize = Size(ival)

	return
}
