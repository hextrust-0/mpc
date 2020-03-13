//
// garble.go
//
// Copyright (c) 2019 Markku Rossi
//
// All rights reserved.
//

package circuit

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"fmt"
	"sort"

	"github.com/markkurossi/mpc/ot"
)

var (
	verbose = false
)

type Enc func(a, b, c *ot.Label, t uint32) []byte

type Dec func(a, b *ot.Label, t uint32, data []byte) ([]byte, error)

type TableEntry struct {
	Index int
	Data  []byte
}

type ByIndex []TableEntry

func (a ByIndex) Len() int {
	return len(a)
}

func (a ByIndex) Swap(i, j int) {
	a[i], a[j] = a[j], a[i]
}

func (a ByIndex) Less(i, j int) bool {
	return a[i].Index < a[j].Index
}

func entry(enc Enc, a, b, c *ot.Label, tweak uint32) TableEntry {
	return TableEntry{
		Index: idx(a, b),
		Data:  enc(a, b, c, tweak),
	}
}

func idx(l0, l1 *ot.Label) int {
	if l0 == nil {
		if l1 == nil {
			return 0
		}
		if l1.S() {
			return 1
		} else {
			return 0
		}
	} else if l1 == nil {
		if l0.S() {
			return 1
		} else {
			return 0
		}
	} else {
		var ret int
		if l0.S() {
			ret |= 0x2
		}
		if l1.S() {
			ret |= 0x1
		}
		return ret
	}
}

func encrypt(alg cipher.Block, a, b, c *ot.Label, t uint32) []byte {
	k := makeK(a, b, t)

	crypted := make([]byte, alg.BlockSize())
	alg.Encrypt(crypted, k.Bytes())

	pi := ot.LabelFromData(crypted)
	pi.Xor(k)
	pi.Xor(c)

	return pi.Bytes()
}

func decrypt(alg cipher.Block, a, b *ot.Label, t uint32, encrypted []byte) (
	[]byte, error) {

	k := makeK(a, b, t)

	crypted := make([]byte, alg.BlockSize())
	alg.Encrypt(crypted, k.Bytes())

	c := ot.LabelFromData(encrypted)
	c.Xor(ot.LabelFromData(crypted))
	c.Xor(k)

	return c.Bytes(), nil
}

func makeK(a, b *ot.Label, t uint32) *ot.Label {
	k := a.Copy()
	k.Mul2()

	if b != nil {
		tmp := b.Copy()
		tmp.Mul4()
		k.Xor(tmp)
	}
	k.Xor(ot.NewTweak(t))

	return k
}

func makeLabels(r *ot.Label) (ot.Wire, error) {
	l0, err := ot.NewLabel(rand.Reader)
	if err != nil {
		return ot.Wire{}, err
	}
	l1 := l0.Copy()
	l1.Xor(r)

	return ot.Wire{
		Label0: l0,
		Label1: l1,
	}, nil
}

type Garbled struct {
	Wires ot.Inputs
	Gates [][][]byte
}

func (c *Circuit) Garble(key []byte) (*Garbled, error) {
	// Create R.
	r, err := ot.NewLabel(rand.Reader)
	if err != nil {
		return nil, err
	}
	r.SetS(true)

	garbled := make([][][]byte, c.NumGates)

	alg, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}

	enc := func(a, b, c *ot.Label, t uint32) []byte {
		return encrypt(alg, a, b, c, t)
	}

	// Assign labels to wires.
	wires := make(ot.Inputs)

	for idx, gate := range c.Gates {
		data, err := gate.Garble(wires, enc, r)
		if err != nil {
			return nil, err
		}
		garbled[idx] = data
	}

	// Assign unset wires. Wire can be unset if one of the inputs is
	// not used in the computation
	for i := 0; i < c.NumWires; i++ {
		_, ok := wires[i]
		if !ok {
			w, err := makeLabels(r)
			if err != nil {
				return nil, err
			}
			wires[i] = w
		}
	}

	return &Garbled{
		Wires: wires,
		Gates: garbled,
	}, nil
}

func (g *Gate) Garble(wires ot.Inputs, enc Enc, r *ot.Label) ([][]byte, error) {
	var in []ot.Wire
	var out []ot.Wire
	var err error

	for _, i := range g.Inputs() {
		w, ok := wires[i.ID()]
		if !ok {
			w, err = makeLabels(r)
			if err != nil {
				return nil, err
			}
			wires[i.ID()] = w
		}
		in = append(in, w)
	}

	// Output
	w, ok := wires[g.Output.ID()]
	if ok {
		return nil, fmt.Errorf("gate output already set %d", g.Output)
	}
	if g.Op == XOR {
		l0 := in[0].Label0.Copy()
		l0.Xor(in[1].Label0)

		l1 := in[0].Label0.Copy()
		l1.Xor(in[1].Label1)
		w = ot.Wire{
			Label0: l0,
			Label1: l1,
		}
	} else {
		w, err = makeLabels(r)
		if err != nil {
			return nil, err
		}
	}
	wires[g.Output.ID()] = w
	out = append(out, w)

	var table []TableEntry

	switch g.Op {
	case XOR:
		// Free XOR.

	case AND:
		// a b c
		// -----
		// 0 0 0
		// 0 1 0
		// 1 0 0
		// 1 1 1
		a := in[0]
		b := in[1]
		c := out[0]
		table = append(table, entry(enc, a.Label0, b.Label0, c.Label0, g.ID))
		table = append(table, entry(enc, a.Label0, b.Label1, c.Label0, g.ID))
		table = append(table, entry(enc, a.Label1, b.Label0, c.Label0, g.ID))
		table = append(table, entry(enc, a.Label1, b.Label1, c.Label1, g.ID))

	case OR:
		// a b c
		// -----
		// 0 0 0
		// 0 1 1
		// 1 0 1
		// 1 1 1
		a := in[0]
		b := in[1]
		c := out[0]
		table = append(table, entry(enc, a.Label0, b.Label0, c.Label0, g.ID))
		table = append(table, entry(enc, a.Label0, b.Label1, c.Label1, g.ID))
		table = append(table, entry(enc, a.Label1, b.Label0, c.Label1, g.ID))
		table = append(table, entry(enc, a.Label1, b.Label1, c.Label1, g.ID))

	case INV:
		// a b c
		// -----
		// 0   1
		// 1   0
		a := in[0]
		c := out[0]
		table = append(table, entry(enc, a.Label0, nil, c.Label1, g.ID))
		table = append(table, entry(enc, a.Label1, nil, c.Label0, g.ID))

	default:
		return nil, fmt.Errorf("Invalid operand %s", g.Op)
	}

	sort.Sort(ByIndex(table))

	var result [][]byte
	for _, entry := range table {
		result = append(result, entry.Data)
	}

	return result, nil
}
