//
// eval.go
//
// Copyright (c) 2019 Markku Rossi
//
// All rights reserved.
//

package circuit

import (
	"crypto/aes"
	"fmt"

	"github.com/markkurossi/mpc/ot"
)

func (c *Circuit) Eval(key []byte, wires map[Wire]*ot.Label,
	garbled [][][]byte) error {

	alg, err := aes.NewCipher(key)
	if err != nil {
		return err
	}

	dec := func(a, b *ot.Label, t uint32, data []byte) ([]byte, error) {
		return decrypt(alg, a, b, t, data)
	}

	for idx, gate := range c.Gates {
		output, err := gate.Eval(wires, dec, garbled[idx])
		if err != nil {
			return err
		}
		wires[gate.Output] = ot.LabelFromData(output)
	}

	return nil
}

func (g *Gate) Eval(wires map[Wire]*ot.Label, dec Dec, garbled [][]byte) (
	[]byte, error) {

	var a *ot.Label
	var aOK bool
	var b *ot.Label
	var bOK bool

	switch g.Op {
	case XOR, AND, OR:
		a, aOK = wires[g.Input0]
		b, bOK = wires[g.Input1]

	case INV:
		a, aOK = wires[g.Input0]
		b = nil
		bOK = true

	default:
		return nil, fmt.Errorf("Invalid operation %s", g.Op)
	}

	if !aOK {
		return nil, fmt.Errorf("No input for wire a found")
	}
	if !bOK {
		return nil, fmt.Errorf("No input for wire b found")
	}

	if g.Op == XOR {
		result := a.Copy()
		result.Xor(b)
		return result.Bytes(), nil
	}

	i := idx(a, b)
	if i >= len(garbled) {
		return nil, fmt.Errorf("corrupted circuit: index %d >= len garbled %d",
			i, len(garbled))
	}

	return dec(a, b, g.ID, garbled[i])
}
