//
// main.go
//
// Copyright (c) 2019-2023 Markku Rossi
//
// All rights reserved.
//

package main

import (
	"flag"
	"fmt"
	"io"
	"log"
	"math/big"
	"net"
	"os"
	"runtime"
	"runtime/pprof"
	"strings"
	"unicode"

	"github.com/markkurossi/mpc/circuit"
	"github.com/markkurossi/mpc/compiler"
	"github.com/markkurossi/mpc/compiler/utils"
	"github.com/markkurossi/mpc/ot"
	"github.com/markkurossi/mpc/p2p"
	"github.com/markkurossi/mpc/types"
)

var (
	port    = ":8080"
	verbose = false
)

type input []string

func (i *input) String() string {
	return fmt.Sprint(*i)
}

func (i *input) Set(value string) error {
	for _, v := range strings.Split(value, ",") {
		*i = append(*i, v)
	}
	return nil
}

var inputFlag input

func init() {
	flag.Var(&inputFlag, "i", "comma-separated list of circuit inputs")
}

func main() {
	evaluator := flag.Bool("e", false, "evaluator / garbler mode")
	stream := flag.Bool("stream", false, "streaming mode")
	compile := flag.Bool("circ", false, "compile MPCL to circuit")
	circFormat := flag.String("format", "mpclc",
		"circuit format: mpclc, bristol")
	ssa := flag.Bool("ssa", false, "compile MPCL to SSA assembly")
	dot := flag.Bool("dot", false, "create Graphviz DOT output")
	svg := flag.Bool("svg", false, "create SVG output")
	optimize := flag.Int("O", 1, "optimization level")
	fVerbose := flag.Bool("v", false, "verbose output")
	fDiagnostics := flag.Bool("d", false, "diagnostics output")
	cpuprofile := flag.String("cpuprofile", "", "write cpu profile to `file`")
	memprofile := flag.String("memprofile", "",
		"write memory profile to `file`")
	bmr := flag.Int("bmr", -1, "semi-honest secure BMR protocol player number")
	flag.Parse()

	log.SetFlags(0)

	verbose = *fVerbose

	if len(*cpuprofile) > 0 {
		f, err := os.Create(*cpuprofile)
		if err != nil {
			log.Fatal("could not create CPU profile: ", err)
		}
		defer f.Close()
		if err := pprof.StartCPUProfile(f); err != nil {
			log.Fatal("could not start CPU profile: ", err)
		}
		defer pprof.StopCPUProfile()
	}

	params := utils.NewParams()
	defer params.Close()

	params.Verbose = *fVerbose
	params.Diagnostics = *fDiagnostics

	if *optimize > 0 {
		params.OptPruneGates = true
	}
	if *ssa && !*compile {
		params.NoCircCompile = true
	}

	if *compile || *ssa {
		err := compileFiles(flag.Args(), params, *compile, *ssa, *dot, *svg,
			*circFormat)
		if err != nil {
			fmt.Printf("compile failed: %s\n", err)
			os.Exit(1)
		}
		memProfile(*memprofile)
		return
	}

	var err error

	oti := ot.NewCO()

	if *stream {
		if *evaluator {
			err = streamEvaluatorMode(oti, inputFlag, len(*cpuprofile) > 0)
		} else {
			err = streamGarblerMode(params, oti, inputFlag, flag.Args())
		}
		memProfile(*memprofile)
		if err != nil {
			fmt.Printf("%s\n", err)
			os.Exit(1)
		}
		return
	}

	if len(flag.Args()) != 1 {
		fmt.Printf("expected one input file, got %v\n", len(flag.Args()))
		os.Exit(1)
	}

	var circ *circuit.Circuit

	file := flag.Args()[0]
	if circuit.IsFilename(file) {
		circ, err = circuit.Parse(file)
		if err != nil {
			fmt.Printf("failed to parse circuit file '%s': %s\n", file, err)
			return
		}
	} else if strings.HasSuffix(file, ".mpcl") {
		circ, _, err = compiler.New(params).CompileFile(file)
		if err != nil {
			fmt.Printf("%s\n", err)
			return
		}
	} else {
		fmt.Printf("unknown file type '%s'\n", file)
		return
	}

	if circ != nil {
		circ.AssignLevels()
		if verbose {
			fmt.Printf("circuit: %v\n", circ)
		}
	}

	var input *big.Int

	if *bmr >= 0 {
		fmt.Printf("semi-honest secure BMR protocol\n")
		fmt.Printf("player: %d\n", *bmr)

		if *bmr >= len(circ.Inputs) {
			fmt.Printf("invalid party number %d for %d-party computation\n",
				*bmr, len(circ.Inputs))
			return
		}

		input, err = circ.Inputs[*bmr].Parse(inputFlag)
		if err != nil {
			fmt.Printf("%s\n", err)
			os.Exit(1)
		}

		for idx, arg := range circ.Inputs {
			if idx == *bmr {
				fmt.Printf(" + In%d: %s\n", idx, arg)
			} else {
				fmt.Printf(" - In%d: %s\n", idx, arg)
			}
		}

		fmt.Printf(" - Out: %s\n", circ.Outputs)
		fmt.Printf(" - In:  %s\n", inputFlag)

		err := bmrMode(circ, input, *bmr)
		if err != nil {
			fmt.Printf("BMR mode failed: %s\n", err)
			os.Exit(1)
		}
		return
	}

	if len(circ.Inputs) != 2 {
		fmt.Printf("invalid circuit for 2-party computation: %d parties\n",
			len(circ.Inputs))
		return
	}

	var i1t, i2t string
	if *evaluator {
		i1t = "- "
		i2t = "+ "
	} else {
		i1t = "+ "
		i2t = "- "
	}

	fmt.Printf(" %sIn1: %s\n", i1t, circ.Inputs[0])
	fmt.Printf(" %sIn2: %s\n", i2t, circ.Inputs[1])
	fmt.Printf(" - Out: %s\n", circ.Outputs)
	fmt.Printf(" -  In: %s\n", inputFlag)

	if *evaluator {
		input, err = circ.Inputs[1].Parse(inputFlag)
		if err != nil {
			fmt.Printf("%s\n", err)
			os.Exit(1)
		}
		err = evaluatorMode(oti, circ, input, len(*cpuprofile) > 0)
	} else {
		input, err = circ.Inputs[0].Parse(inputFlag)
		if err != nil {
			fmt.Printf("%s\n", err)
			os.Exit(1)
		}
		err = garblerMode(oti, circ, input)
	}
	if err != nil {
		log.Fatal(err)
	}
}

func memProfile(file string) {
	if len(file) == 0 {
		return
	}

	f, err := os.Create(file)
	if err != nil {
		log.Fatal("could not create memory profile: ", err)
	}
	defer f.Close()
	if false {
		runtime.GC()
	}
	if err := pprof.WriteHeapProfile(f); err != nil {
		log.Fatal("could not write memory profile: ", err)
	}
}

func evaluatorMode(oti ot.OT, circ *circuit.Circuit, input *big.Int,
	once bool) error {
	ln, err := net.Listen("tcp", port)
	if err != nil {
		return err
	}
	fmt.Printf("Listening for connections at %s\n", port)

	for {
		nc, err := ln.Accept()
		if err != nil {
			return err
		}
		fmt.Printf("New connection from %s\n", nc.RemoteAddr())

		conn := p2p.NewConn(nc)
		result, err := circuit.Evaluator(conn, oti, circ, input, verbose)
		conn.Close()

		if err != nil && err != io.EOF {
			return err
		}

		printResults(result, circ.Outputs)
		if once {
			return nil
		}
	}
}

func garblerMode(oti ot.OT, circ *circuit.Circuit, input *big.Int) error {
	nc, err := net.Dial("tcp", port)
	if err != nil {
		return err
	}
	conn := p2p.NewConn(nc)
	defer conn.Close()

	result, err := circuit.Garbler(conn, oti, circ, input, verbose)
	if err != nil {
		return err
	}
	printResults(result, circ.Outputs)

	return nil
}

func printResults(results []*big.Int, outputs circuit.IO) {
	for idx, result := range results {
		if outputs == nil {
			fmt.Printf("Result[%d]: %v\n", idx, result)
			fmt.Printf("Result[%d]: 0b%s\n", idx, result.Text(2))
			bytes := result.Bytes()
			if len(bytes) == 0 {
				bytes = []byte{0}
			}
			fmt.Printf("Result[%d]: 0x%x\n", idx, bytes)
		} else {
			fmt.Printf("Result[%d]: %s\n", idx,
				printResult(result, outputs[idx], false))
		}
	}
}

func printResult(result *big.Int, output circuit.IOArg, short bool) string {
	var str string

	switch output.Type.Type {
	case types.TString:
		mask := big.NewInt(0xff)

		for i := 0; i < int(output.Type.Bits)/8; i++ {
			tmp := new(big.Int).Rsh(result, uint(i*8))
			r := rune(tmp.And(tmp, mask).Uint64())
			if unicode.IsPrint(r) {
				str += string(r)
			} else {
				str += fmt.Sprintf("\\u%04x", r)
			}
		}

	case types.TUint, types.TInt:
		if output.Type.Type == types.TInt {
			bits := int(output.Type.Bits)
			if result.Bit(bits-1) == 1 {
				// Negative number.
				tmp := new(big.Int)
				tmp.SetBit(tmp, bits, 1)
				result.Sub(tmp, result)
				result.Neg(result)
			}
		}

		bytes := result.Bytes()
		if len(bytes) == 0 {
			bytes = []byte{0}
		}
		if short {
			str = fmt.Sprintf("%v", result)
		} else if output.Type.Bits <= 64 {
			str = fmt.Sprintf("0x%x\t%v", bytes, result)
		} else {
			str = fmt.Sprintf("0x%x", bytes)
		}

	case types.TBool:
		str = fmt.Sprintf("%v", result.Uint64() != 0)

	case types.TArray:
		count := int(output.Type.ArraySize)
		elSize := int(output.Type.ElementType.Bits)

		mask := new(big.Int)
		for i := 0; i < elSize; i++ {
			mask.SetBit(mask, i, 1)
		}

		var hexString bool
		if output.Type.ElementType.Type == types.TUint &&
			output.Type.ElementType.Bits == 8 {
			hexString = true
		}
		if !hexString {
			str = "["
		}
		for i := 0; i < count; i++ {
			r := new(big.Int).Rsh(result, uint(i*elSize))
			r = r.And(r, mask)

			if hexString {
				str += fmt.Sprintf("%02x", r.Int64())
			} else {
				if i > 0 {
					str += " "
				}
				str += printResult(r, circuit.IOArg{
					Type: *output.Type.ElementType,
				}, true)
			}
		}
		if !hexString {
			str += "]"
		}

	default:
		str = fmt.Sprintf("%v (%s)", result, output.Type)
	}

	return str
}
