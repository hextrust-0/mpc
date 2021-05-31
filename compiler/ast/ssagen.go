//
// Copyright (c) 2019-2021 Markku Rossi
//
// All rights reserved.
//

package ast

import (
	"fmt"

	"github.com/markkurossi/mpc/compiler/ssa"
	"github.com/markkurossi/mpc/compiler/types"
)

// SSA implements the compiler.ast.AST.SSA for list statements.
func (ast List) SSA(block *ssa.Block, ctx *Codegen, gen *ssa.Generator) (
	*ssa.Block, []ssa.Variable, error) {

	var err error

	for _, b := range ast {
		if block.Dead {
			ctx.logger.Warningf(b.Location(), "unreachable code")
			break
		}
		block, _, err = b.SSA(block, ctx, gen)
		if err != nil {
			return nil, nil, err
		}
	}

	return block, nil, nil
}

// SSA implements the compiler.ast.AST.SSA for function definitions.
func (ast *Func) SSA(block *ssa.Block, ctx *Codegen, gen *ssa.Generator) (
	*ssa.Block, []ssa.Variable, error) {

	ctx.Start().Name = fmt.Sprintf("%s#%d", ast.Name, ast.NumInstances)
	ctx.Return().Name = fmt.Sprintf("%s.ret#%d", ast.Name, ast.NumInstances)
	ast.NumInstances++

	// Define return variables.
	for idx, ret := range ast.Return {
		if len(ret.Name) == 0 {
			ret.Name = fmt.Sprintf("%%ret%d", idx)
		}
		typeInfo, err := ret.Type.Resolve(NewEnv(block), ctx, gen)
		if err != nil {
			return nil, nil, ctx.logger.Errorf(ret.Loc,
				"invalid return type: %s", err)
		}
		r, err := gen.NewVar(ret.Name, typeInfo, ctx.Scope())
		if err != nil {
			return nil, nil, err
		}
		block.Bindings.Set(r, nil)
	}

	block, _, err := ast.Body.SSA(block, ctx, gen)
	if err != nil {
		return nil, nil, err
	}

	// Select return variables.
	var vars []ssa.Variable
	for _, ret := range ast.Return {
		v, ok := ctx.Start().ReturnBinding(ssa.NewReturnBindingCTX(), ret.Name,
			ctx.Return(), gen)
		if !ok {
			return nil, nil, ctx.logger.Errorf(ast.Loc,
				"undefined variable '%s'", ret.Name)
		}
		vars = append(vars, v)
	}

	caller := ctx.Caller()
	if caller == nil {
		ctx.Return().AddInstr(ssa.NewRetInstr(vars))
	}

	return block, vars, nil
}

// SSA implements the compiler.ast.AST.SSA for constant definitions.
func (ast *ConstantDef) SSA(block *ssa.Block, ctx *Codegen,
	gen *ssa.Generator) (*ssa.Block, []ssa.Variable, error) {

	typeInfo, err := ast.Type.Resolve(NewEnv(block), ctx, gen)
	if err != nil {
		return nil, nil, err
	}

	env := NewEnv(block)

	constVal, ok, err := ast.Init.Eval(env, ctx, gen)
	if err != nil {
		return nil, nil, err
	}
	if !ok {
		return nil, nil, ctx.logger.Errorf(ast.Init.Location(),
			"init value is not constant")
	}
	constVar, err := ssa.Constant(gen, constVal, typeInfo)
	if err != nil {
		return nil, nil, err
	}
	if typeInfo.Type == types.Undefined {
		typeInfo.Type = constVar.Type.Type
	}
	if typeInfo.Bits == 0 {
		typeInfo.Bits = constVar.Type.Bits
	}
	if !typeInfo.CanAssignConst(constVar.Type) {
		return nil, nil, ctx.logger.Errorf(ast.Init.Location(),
			"invalid init value %s for type %s", constVar.Type, typeInfo)
	}

	_, ok = block.Bindings.Get(ast.Name)
	if ok {
		return nil, nil, ctx.logger.Errorf(ast.Loc,
			"constant %s already defined", ast.Name)
	}
	lValue := constVar
	lValue.Name = ast.Name
	block.Bindings.Set(lValue, &constVar)

	return block, nil, nil
}

// SSA implements the compiler.ast.AST.SSA for variable definitions.
func (ast *VariableDef) SSA(block *ssa.Block, ctx *Codegen,
	gen *ssa.Generator) (*ssa.Block, []ssa.Variable, error) {

	typeInfo, err := ast.Type.Resolve(NewEnv(block), ctx, gen)
	if err != nil {
		return nil, nil, ctx.logger.Errorf(ast.Location(),
			"invalid variable type: %s", err)
	}

	for _, n := range ast.Names {
		lValue, err := gen.NewVar(n, typeInfo, ctx.Scope())
		if err != nil {
			return nil, nil, err
		}
		block.Bindings.Set(lValue, nil)

		var init ssa.Variable
		if ast.Init == nil {
			initVal, err := initValue(lValue.Type)
			if err != nil {
				return nil, nil, ctx.logger.Errorf(ast.Loc, "%s", err)
			}
			init, err = ssa.Constant(gen, initVal, typeInfo)
			if err != nil {
				return nil, nil, err
			}
			gen.AddConstant(init)
		} else {
			var v []ssa.Variable
			block, v, err = ast.Init.SSA(block, ctx, gen)
			if err != nil {
				return nil, nil, err
			}
			if len(v) != 1 {
				return nil, nil, ctx.logger.Errorf(ast.Loc,
					"multiple-value %s used in single-value context", ast.Init)
			}
			init = v[0]
		}
		block.AddInstr(ssa.NewMovInstr(init, lValue))
	}
	return block, nil, nil
}

func initValue(typeInfo types.Info) (interface{}, error) {
	switch typeInfo.Type {
	case types.Bool:
		return false, nil
	case types.Int, types.Uint:
		return int32(0), nil
	case types.String:
		return "", nil
	case types.Array:
		elInit, err := initValue(*typeInfo.ArrayElement)
		if err != nil {
			return nil, err
		}
		init := make([]interface{}, typeInfo.ArraySize)
		for i := 0; i < typeInfo.ArraySize; i++ {
			init[i] = elInit
		}
		return init, nil
	default:
		return nil, fmt.Errorf("unsupported variable type: %s", typeInfo.Type)
	}
}

// SSA implements the compiler.ast.AST.SSA for assignment expressions.
func (ast *Assign) SSA(block *ssa.Block, ctx *Codegen,
	gen *ssa.Generator) (*ssa.Block, []ssa.Variable, error) {

	var values []ssa.Variable
	var err error

	for _, expr := range ast.Exprs {
		// Check if init value is constant.
		env := NewEnv(block)
		constVal, ok, err := expr.Eval(env, ctx, gen)
		if err != nil {
			return nil, nil, err
		}
		if ok {
			constVar, err := ssa.Constant(gen, constVal, types.UndefinedInfo)
			if err != nil {
				return nil, nil, err
			}
			values = append(values, constVar)
		} else {
			var v []ssa.Variable
			block, v, err = expr.SSA(block, ctx, gen)
			if err != nil {
				return nil, nil, err
			}
			if len(v) == 0 {
				return nil, nil, ctx.logger.Errorf(expr.Location(),
					"%s used as value", expr)
			}
			values = append(values, v...)
		}
	}
	if len(ast.LValues) != len(values) {
		return nil, nil, ctx.logger.Errorf(ast.Loc,
			"assignment mismatch: %d variables but %d value",
			len(values), len(ast.LValues))
	}

	for idx, lvalue := range ast.LValues {
		switch lv := lvalue.(type) {
		case *VariableRef:
			// XXX package.name below
			var lValue ssa.Variable
			b, ok := block.Bindings.Get(lv.Name.Name)
			if ast.Define {
				if ok {
					return nil, nil, ctx.logger.Errorf(ast.Loc,
						"no new variables on left side of :=")
				}
				lValue, err = gen.NewVar(lv.Name.Name, values[idx].Type,
					ctx.Scope())
				if err != nil {
					return nil, nil, err
				}
			} else {
				if !ok {
					return nil, nil, ctx.logger.Errorf(ast.Loc,
						"undefined: %s", lv.Name)
				}
				lValue, err = gen.NewVar(b.Name, b.Type, ctx.Scope())
				if err != nil {
					return nil, nil, err
				}
			}

			block.AddInstr(ssa.NewMovInstr(values[idx], lValue))
			block.Bindings.Set(lValue, &values[idx])

		case *Index:
			if ast.Define {
				return nil, nil, ctx.logger.Errorf(ast.Loc,
					"a non-name %s on left side of :=", lv)
			}
			switch arr := lv.Expr.(type) {
			case *VariableRef:
				// XXX package.name below
				b, ok := block.Bindings.Get(arr.Name.Name)
				if !ok {
					return nil, nil, ctx.logger.Errorf(ast.Loc,
						"undefined: %s", arr.Name)
				}
				lValue, err := gen.NewVar(b.Name, b.Type, ctx.Scope())
				if err != nil {
					return nil, nil, err
				}

				block, val, err := lv.Index.SSA(block, ctx, gen)
				if err != nil {
					return nil, nil, err
				}
				if len(val) != 1 || !val[0].Const {
					return nil, nil, ctx.logger.Errorf(lv.Index.Location(),
						"invalid index")
				}
				var index int
				switch v := val[0].ConstValue.(type) {
				case int32:
					index = int(v)
				default:
					return nil, nil, ctx.logger.Errorf(lv.Index.Location(),
						"invalid index: %T", v)
				}

				// Convert index to bit range.
				if index >= b.Type.ArraySize {
					return nil, nil, ctx.logger.Errorf(lv.Index.Location(),
						"invalid array index %d (out of bounds for %d-element array)",
						index, b.Type.ArraySize)
				}
				from := int32(index * b.Type.ArrayElement.Bits)
				to := int32((index + 1) * b.Type.ArrayElement.Bits)

				indexType := types.Info{
					Type:    types.Uint,
					Bits:    32,
					MinBits: 32,
				}
				fromConst, err := ssa.Constant(gen, from, indexType)
				if err != nil {
					return nil, nil, err
				}
				toConst, err := ssa.Constant(gen, to, indexType)
				if err != nil {
					return nil, nil, err
				}

				block.AddInstr(ssa.NewAmovInstr(values[idx],
					b.Value(block, gen), fromConst, toConst, lValue))
				block.Bindings.Set(lValue, nil)

				return block, []ssa.Variable{lValue}, nil

			default:
				return nil, nil, ctx.logger.Errorf(ast.Loc,
					"array expression not supported: %T", arr)
			}

		default:
			return nil, nil, ctx.logger.Errorf(ast.Loc,
				"cannot assign to %s (%T)", lv, lv)
		}
	}

	return block, values, nil
}

// SSA implements the compiler.ast.AST.SSA for if statements.
func (ast *If) SSA(block *ssa.Block, ctx *Codegen, gen *ssa.Generator) (
	*ssa.Block, []ssa.Variable, error) {

	env := NewEnv(block)
	constVal, ok, err := ast.Expr.Eval(env, ctx, gen)
	if err != nil {
		return nil, nil, err
	}
	if ok {
		block.Bindings = env.Bindings
		val, ok := constVal.(bool)
		if !ok {
			return nil, nil, ctx.logger.Errorf(ast.Expr.Location(),
				"condition is not boolean expression")
		}
		if val {
			return ast.True.SSA(block, ctx, gen)
		} else if ast.False != nil {
			return ast.False.SSA(block, ctx, gen)
		}
		return block, nil, nil
	}

	block, e, err := ast.Expr.SSA(block, ctx, gen)
	if err != nil {
		return nil, nil, err
	}
	if len(e) == 0 {
		return nil, nil, ctx.logger.Errorf(ast.Expr.Location(),
			"%s used as value", ast.Expr)
	} else if len(e) > 1 {
		return nil, nil, ctx.logger.Errorf(ast.Expr.Location(),
			"multiple-value %s used in single-value context", ast.Expr)
	}
	if e[0].Type.Type != types.Bool {
		return nil, nil, ctx.logger.Errorf(ast.Expr.Location(),
			"non-bool %s (type %s) used as if condition", ast.Expr, e[0].Type)
	}

	block.BranchCond = e[0]

	// Branch.
	tBlock := gen.BranchBlock(block)

	// True branch.
	tNext, _, err := ast.True.SSA(tBlock, ctx, gen)
	if err != nil {
		return nil, nil, err
	}

	// False (else) branch.
	if len(ast.False) == 0 {
		// No else branch.
		if tNext.Dead {
			// True branch terminated.
			tNext = gen.NextBlock(block)
		} else {
			tNext.Bindings = tNext.Bindings.Merge(e[0], block.Bindings)
			block.SetNext(tNext)
		}

		return tNext, nil, nil
	}

	fBlock := gen.NextBlock(block)

	fNext, _, err := ast.False.SSA(fBlock, ctx, gen)
	if err != nil {
		return nil, nil, err
	}

	if fNext.Dead && tNext.Dead {
		// Both branches terminate.
		next := gen.Block()
		next.Dead = true
		return next, nil, nil
	} else if fNext.Dead {
		// False-branch terminates.
		return tNext, nil, nil
	} else if tNext.Dead {
		// True-branch terminates.
		return fNext, nil, nil
	}

	// Both branches continue.

	next := gen.Block()
	tNext.SetNext(next)

	fNext.SetNext(next)

	next.Bindings = tNext.Bindings.Merge(e[0], fNext.Bindings)

	return next, nil, nil
}

// SSA implements the compiler.ast.AST.SSA for call expressions.
func (ast *Call) SSA(block *ssa.Block, ctx *Codegen, gen *ssa.Generator) (
	*ssa.Block, []ssa.Variable, error) {

	// Generate call values.

	var callValues [][]ssa.Variable
	var v []ssa.Variable
	var err error

	for _, expr := range ast.Exprs {
		block, v, err = expr.SSA(block, ctx, gen)
		if err != nil {
			return nil, nil, err
		}
		callValues = append(callValues, v)
	}

	// Resolve called.
	var pkgName string
	if len(ast.Ref.Name.Package) > 0 {
		pkgName = ast.Ref.Name.Package
	} else {
		pkgName = ast.Ref.Name.Defined
	}
	pkg, ok := ctx.Packages[pkgName]
	if !ok {
		return nil, nil,
			ctx.logger.Errorf(ast.Loc, "package '%s' not found", pkgName)
	}
	called, ok := pkg.Functions[ast.Ref.Name.Name]
	if !ok {
		// Check builtin functions.
		for _, bi := range builtins {
			if bi.Name != ast.Ref.Name.Name {
				continue
			}
			if bi.Type != BuiltinFunc || bi.SSA == nil {
				return nil, nil, ctx.logger.Errorf(ast.Loc,
					"builtin %s used as function", bi.Name)
			}
			// Flatten arguments.
			var args []ssa.Variable
			for _, arg := range callValues {
				args = append(args, arg...)
			}
			return bi.SSA(block, ctx, gen, args, ast.Loc)
		}

		// Resolve name as type.
		typeName := &TypeInfo{
			Type: TypeName,
			Name: ast.Ref.Name,
		}
		typeInfo, err := typeName.Resolve(NewEnv(block), ctx, gen)
		if err != nil {
			return nil, nil, ctx.logger.Errorf(ast.Loc, "undefined: %s",
				ast.Ref)
		}
		if len(callValues) != 1 {
			return nil, nil, ctx.logger.Errorf(ast.Loc, "undefined: %s",
				ast.Ref)
		}
		if len(callValues[0]) == 0 {
			return nil, nil, ctx.logger.Errorf(ast.Exprs[0].Location(),
				"%s used as value", ast.Exprs[0])
		}
		if len(callValues[0]) > 1 {
			return nil, nil, ctx.logger.Errorf(ast.Exprs[0].Location(),
				"multiple-value %s in single-value context", ast.Exprs[0])
		}

		// Convert value to type
		t := gen.AnonVar(typeInfo)
		block.AddInstr(ssa.NewMovInstr(callValues[0][0], t))

		return block, []ssa.Variable{t}, nil
	}

	var args []ssa.Variable

	if len(callValues) == 0 {
		if len(called.Args) != 0 {
			return nil, nil, ctx.logger.Errorf(ast.Loc,
				"not enough arguments in call to %s", ast.Ref)
			// TODO \thave ()
			// TODO \twant (int, int)
		}
	} else if len(callValues) == 1 {
		if len(callValues[0]) < len(called.Args) {
			return nil, nil, ctx.logger.Errorf(ast.Loc,
				"not enough arguments in call to %s", ast.Ref)
			// TODO \thave ()
			// TODO \twant (int, int)
		} else if len(callValues[0]) > len(called.Args) {
			return nil, nil, ctx.logger.Errorf(ast.Loc,
				"too many arguments in call to %s", ast.Ref)
			// TODO \thave (int, int)
			// TODO \twant ()
		}
		args = callValues[0]
	} else {
		if len(callValues) < len(called.Args) {
			return nil, nil, ctx.logger.Errorf(ast.Loc,
				"not enough arguments in call to %s", ast.Ref)
			// TODO \thave ()
			// TODO \twant (int, int)
		} else if len(callValues) > len(called.Args) {
			return nil, nil, ctx.logger.Errorf(ast.Loc,
				"too many arguments in call to %s", ast.Ref)
			// TODO \thave (int, int)
			// TODO \twant ()
		} else {
			for idx, ca := range callValues {
				expr := ast.Exprs[idx]
				if len(ca) == 0 {
					return nil, nil,
						ctx.logger.Errorf(expr.Location(),
							"%s used as value", expr)
				} else if len(ca) > 1 {
					return nil, nil,
						ctx.logger.Errorf(expr.Location(),
							"multiple-value %s in single-value context", expr)
				}
				args = append(args, ca[0])
			}
		}
	}

	// Return block.
	rblock := gen.Block()
	rblock.Bindings = block.Bindings.Clone()

	ctx.PushCompilation(gen.Block(), gen.Block(), rblock, called)

	// Define arguments.
	for idx, arg := range called.Args {
		typeInfo, err := arg.Type.Resolve(NewEnv(block), ctx, gen)
		if err != nil {
			return nil, nil, ctx.logger.Errorf(arg.Loc,
				"invalid argument type: %s", err)
		}
		if typeInfo.Bits == 0 {
			typeInfo.Bits = args[idx].Type.Bits
		}
		a, err := gen.NewVar(arg.Name, typeInfo, ctx.Scope())
		if err != nil {
			return nil, nil, err
		}
		if !a.TypeCompatible(args[idx]) {
			return nil, nil, ctx.logger.Errorf(ast.Location(),
				"invalid value %v for argument %d of %s",
				args[idx].Type, idx, called)
		}
		ctx.Start().Bindings.Set(a, &args[idx])

		block.AddInstr(ssa.NewMovInstr(args[idx], a))
	}

	// Instantiate called function.
	_, returnValues, err := called.SSA(ctx.Start(), ctx, gen)
	if err != nil {
		return nil, nil, err
	}

	block.SetNext(ctx.Start())

	ctx.Return().SetNext(rblock)
	block = rblock

	ctx.PopCompilation()

	return block, returnValues, nil
}

// SSA implements the compiler.ast.AST.SSA for return statements.
func (ast *Return) SSA(block *ssa.Block, ctx *Codegen, gen *ssa.Generator) (
	*ssa.Block, []ssa.Variable, error) {

	if ctx.Func() == nil {
		return nil, nil, ctx.logger.Errorf(ast.Loc, "return outside function")
	}

	var rValues [][]ssa.Variable
	var result []ssa.Variable
	var v []ssa.Variable
	var err error

	for _, expr := range ast.Exprs {
		block, v, err = expr.SSA(block, ctx, gen)
		if err != nil {
			return nil, nil, err
		}
		rValues = append(rValues, v)
	}
	if len(rValues) == 0 {
		if len(ctx.Func().Return) != 0 {
			return nil, nil, ctx.logger.Errorf(ast.Loc,
				"not enough arguments to return")
			// TODO \thave ()
			// TODO \twant (error)
		}
	} else if len(rValues) == 1 {
		if len(rValues[0]) < len(ctx.Func().Return) {
			return nil, nil, ctx.logger.Errorf(ast.Loc,
				"not enough arguments to return")
			// TODO \thave ()
			// TODO \twant (error)
		} else if len(rValues[0]) > len(ctx.Func().Return) {
			return nil, nil, ctx.logger.Errorf(ast.Loc,
				"too many aruments to return")
			// TODO \thave (nil, error)
			// TODO \twant (error)
		}
		result = rValues[0]
	} else {
		if len(rValues) < len(ctx.Func().Return) {
			return nil, nil, ctx.logger.Errorf(ast.Loc,
				"not enough arguments to return")
			// TODO \thave ()
			// TODO \twant (error)
		} else if len(rValues) > len(ctx.Func().Return) {
			return nil, nil, ctx.logger.Errorf(ast.Loc,
				"too many aruments to return")
			// TODO \thave (nil, error)
			// TODO \twant (error)
		} else {
			for idx, rv := range rValues {
				expr := ast.Exprs[idx]
				if len(rv) == 0 {
					return nil, nil,
						ctx.logger.Errorf(expr.Location(),
							"%s used as value", expr)
				} else if len(rv) > 1 {
					return nil, nil,
						ctx.logger.Errorf(expr.Location(),
							"multiple-value %s in single-value context", expr)
				}
				result = append(result, rv[0])
			}
		}
	}

	for idx, r := range ctx.Func().Return {
		typeInfo, err := r.Type.Resolve(NewEnv(block), ctx, gen)
		if err != nil {
			return nil, nil, ctx.logger.Errorf(r.Loc,
				"invalid return type: %s", err)
		}
		if typeInfo.Bits == 0 {
			typeInfo.Bits = result[idx].Type.Bits
		}
		v, err := gen.NewVar(r.Name, typeInfo, ctx.Scope())
		if err != nil {
			return nil, nil, err
		}

		if result[idx].Type.Type == types.Undefined {
			result[idx].Type.Type = typeInfo.Type
		}

		if !v.TypeCompatible(result[idx]) {
			return nil, nil, ctx.logger.Errorf(ast.Location(),
				"invalid value %v for result value %v",
				result[idx].Type, v.Type)
		}

		block.AddInstr(ssa.NewMovInstr(result[idx], v))
		block.Bindings.Set(v, nil)
	}

	block.SetNext(ctx.Return())
	block.Dead = true

	return block, nil, nil
}

// SSA implements the compiler.ast.AST.SSA for for statements.
func (ast *For) SSA(block *ssa.Block, ctx *Codegen, gen *ssa.Generator) (
	*ssa.Block, []ssa.Variable, error) {

	// Use the same env for the whole for-loop unrolling.
	env := NewEnv(block)

	// Init loop.
	_, ok, err := ast.Init.Eval(env, ctx, gen)
	if err != nil {
		return nil, nil, err
	}
	if !ok {
		return nil, nil, ctx.logger.Errorf(ast.Init.Location(),
			"init statement is not compile-time constant: %s", err)
	}

	// Expand body as long as condition is true.
	for i := 0; ; i++ {
		if i >= gen.Params.MaxLoopUnroll {
			return nil, nil, ctx.logger.Errorf(ast.Location(),
				"for-loop unroll limit exceeded: %d", i)
		}
		constVal, ok, err := ast.Cond.Eval(env, ctx, gen)
		if err != nil {
			return nil, nil, err
		}
		if !ok {
			return nil, nil, ctx.logger.Errorf(ast.Cond.Location(),
				"condition is not compile-time constant: %s", ast.Cond)
		}
		val, ok := constVal.(bool)
		if !ok {
			return nil, nil, ctx.logger.Errorf(ast.Cond.Location(),
				"condition is not boolean expression")
		}
		if !val {
			// Loop completed.
			break
		}
		block.Bindings = env.Bindings

		// Expand block.
		block, _, err = ast.Body.SSA(block, ctx, gen)
		if err != nil {
			return nil, nil, err
		}

		// Increment.
		env = NewEnv(block)
		_, ok, err = ast.Inc.Eval(env, ctx, gen)
		if err != nil {
			return nil, nil, err
		}
		if !ok {
			return nil, nil, ctx.logger.Errorf(ast.Init.Location(),
				"increment statement is not compile-time constant: %s", ast.Inc)
		}
	}

	return block, nil, nil
}

// SSA implements the compiler.ast.AST.SSA for binary expressions.
func (ast *Binary) SSA(block *ssa.Block, ctx *Codegen, gen *ssa.Generator) (
	*ssa.Block, []ssa.Variable, error) {

	// Check constant folding.
	constVal, ok, err := ast.Eval(NewEnv(block), ctx, gen)
	if err != nil {
		return nil, nil, err
	}
	if ok {
		if ctx.Verbose {
			fmt.Printf("ConstFold: %v %s %v => %v\n",
				ast.Left, ast.Op, ast.Right, constVal)
		}
		v, err := ssa.Constant(gen, constVal, types.UndefinedInfo)
		if err != nil {
			return nil, nil, err
		}
		gen.AddConstant(v)
		return block, []ssa.Variable{v}, nil
	}

	block, lArr, err := ast.Left.SSA(block, ctx, gen)
	if err != nil {
		return nil, nil, err
	}

	block, rArr, err := ast.Right.SSA(block, ctx, gen)
	if err != nil {
		return nil, nil, err
	}

	// Check that l and r are of same type.
	if len(lArr) == 0 {
		return nil, nil, ctx.logger.Errorf(ast.Left.Location(),
			"%s used as value", ast.Left)
	}
	if len(lArr) > 1 {
		return nil, nil, ctx.logger.Errorf(ast.Left.Location(),
			"multiple-value %s in single-value context", ast.Left)
	}
	if len(rArr) == 0 {
		return nil, nil, ctx.logger.Errorf(ast.Right.Location(),
			"%s used as value", ast.Right)
	}
	if len(rArr) > 1 {
		return nil, nil, ctx.logger.Errorf(ast.Right.Location(),
			"multiple-value %s in single-value context", ast.Right)
	}
	l := lArr[0]
	r := rArr[0]

	if !l.TypeCompatible(r) {
		return nil, nil,
			ctx.logger.Errorf(ast.Loc, "invalid types: %s %s %s",
				l.Type, ast.Op, r.Type)
	}

	// Resolve target type.
	var resultType types.Info
	switch ast.Op {
	case BinaryMult, BinaryDiv, BinaryMod, BinaryLshift, BinaryRshift,
		BinaryBand, BinaryBclear,
		BinaryPlus, BinaryMinus, BinaryBor, BinaryBxor:
		resultType = l.Type

	case BinaryLt, BinaryLe, BinaryGt, BinaryGe, BinaryEq, BinaryNeq,
		BinaryAnd, BinaryOr:
		resultType = types.BoolType()

	default:
		fmt.Printf("%s %s %s\n", l, ast.Op, r)
		return nil, nil, ctx.logger.Errorf(ast.Loc,
			"Binary.SSA '%s' not implemented yet", ast.Op)
	}
	t := gen.AnonVar(resultType)

	var instr ssa.Instr
	switch ast.Op {
	case BinaryMult:
		instr, err = ssa.NewMultInstr(l.Type, l, r, t)
	case BinaryDiv:
		instr, err = ssa.NewDivInstr(l.Type, l, r, t)
	case BinaryMod:
		instr, err = ssa.NewModInstr(l.Type, l, r, t)
	case BinaryLshift:
		instr = ssa.NewLshiftInstr(l, r, t)
	case BinaryRshift:
		instr = ssa.NewRshiftInstr(l, r, t)
	case BinaryBand:
		instr, err = ssa.NewBandInstr(l, r, t)
	case BinaryBclear:
		instr, err = ssa.NewBclrInstr(l, r, t)
	case BinaryPlus:
		instr, err = ssa.NewAddInstr(l.Type, l, r, t)
	case BinaryMinus:
		instr, err = ssa.NewSubInstr(l.Type, l, r, t)
	case BinaryBor:
		instr, err = ssa.NewBorInstr(l, r, t)
	case BinaryBxor:
		instr, err = ssa.NewBxorInstr(l, r, t)
	case BinaryEq:
		instr, err = ssa.NewEqInstr(l, r, t)
	case BinaryNeq:
		instr, err = ssa.NewNeqInstr(l, r, t)
	case BinaryLt:
		instr, err = ssa.NewLtInstr(l.Type, l, r, t)
	case BinaryLe:
		instr, err = ssa.NewLeInstr(l.Type, l, r, t)
	case BinaryGt:
		instr, err = ssa.NewGtInstr(l.Type, l, r, t)
	case BinaryGe:
		instr, err = ssa.NewGeInstr(l.Type, l, r, t)
	case BinaryAnd:
		instr, err = ssa.NewAndInstr(l, r, t)
	case BinaryOr:
		instr, err = ssa.NewOrInstr(l, r, t)
	default:
		fmt.Printf("%s %s %s\n", l, ast.Op, r)
		return nil, nil, ctx.logger.Errorf(ast.Loc,
			"Binary.SSA '%s' not implemented yet", ast.Op)
	}
	if err != nil {
		return nil, nil, err
	}

	block.AddInstr(instr)

	return block, []ssa.Variable{t}, nil
}

// SSA implements the compiler.ast.AST.SSA for slice expressions.
func (ast *Slice) SSA(block *ssa.Block, ctx *Codegen, gen *ssa.Generator) (
	*ssa.Block, []ssa.Variable, error) {

	block, expr, err := ast.Expr.SSA(block, ctx, gen)
	if err != nil {
		return nil, nil, err
	}
	if len(expr) != 1 {
		return nil, nil, ctx.logger.Errorf(ast.Loc, "invalid expression")
	}

	var val []ssa.Variable
	var from int32
	if ast.From == nil {
		from = 0
	} else {
		block, val, err = ast.From.SSA(block, ctx, gen)
		if err != nil {
			return nil, nil, err
		}
		if len(val) != 1 || !val[0].Const {
			return nil, nil, ctx.logger.Errorf(ast.From.Location(),
				"invalid from index")
		}
		switch v := val[0].ConstValue.(type) {
		case int32:
			from = v
		default:
			return nil, nil, ctx.logger.Errorf(ast.From.Location(),
				"invalid from index: %T", v)
		}
	}
	var to int32
	if ast.To == nil {
		to = int32(expr[0].Type.Bits)
	} else {
		block, val, err = ast.To.SSA(block, ctx, gen)
		if err != nil {
			return nil, nil, err
		}
		if len(val) != 1 || !val[0].Const {
			return nil, nil, ctx.logger.Errorf(ast.To.Location(),
				"invalid to index")
		}
		switch v := val[0].ConstValue.(type) {
		case int32:
			to = v
		default:
			return nil, nil, ctx.logger.Errorf(ast.From.Location(),
				"invalid to index: %T", v)
		}
	}
	if from >= int32(expr[0].Type.Bits) || from >= to {
		return nil, nil, ctx.logger.Errorf(ast.Loc,
			"slice bounds out of range [%d:%d]", from, to)
	}

	indexType := types.Info{
		Type:    types.Uint,
		Bits:    32,
		MinBits: 32,
	}
	fromConst, err := ssa.Constant(gen, from, indexType)
	if err != nil {
		return nil, nil, err
	}
	toConst, err := ssa.Constant(gen, to, indexType)
	if err != nil {
		return nil, nil, err
	}

	t := gen.AnonVar(types.Info{
		Type:    expr[0].Type.Type,
		Bits:    int(to - from),
		MinBits: int(to - from),
	})

	block.AddInstr(ssa.NewSliceInstr(expr[0], fromConst, toConst, t))

	return block, []ssa.Variable{t}, nil
}

// SSA implements the compiler.ast.AST.SSA for index expressions.
func (ast *Index) SSA(block *ssa.Block, ctx *Codegen, gen *ssa.Generator) (
	*ssa.Block, []ssa.Variable, error) {

	block, exprs, err := ast.Expr.SSA(block, ctx, gen)
	if err != nil {
		return nil, nil, err
	}
	if len(exprs) != 1 {
		return nil, nil, ctx.logger.Errorf(ast.Loc, "invalid expression")
	}
	expr := exprs[0]

	block, val, err := ast.Index.SSA(block, ctx, gen)
	if err != nil {
		return nil, nil, err
	}
	if len(val) != 1 || !val[0].Const {
		return nil, nil, ctx.logger.Errorf(ast.Index.Location(),
			"invalid index")
	}
	var index int
	switch v := val[0].ConstValue.(type) {
	case int32:
		index = int(v)
	default:
		return nil, nil, ctx.logger.Errorf(ast.Index.Location(),
			"invalid index: %T", v)
	}

	switch expr.Type.Type {
	case types.Array:
		if index < 0 || index >= expr.Type.ArraySize {
			return nil, nil, ctx.logger.Errorf(ast.Index.Location(),
				"invalid array index %d (out of bounds for %d-element array)",
				index, expr.Type.ArraySize)
		}
		from := int32(index * expr.Type.ArrayElement.Bits)
		to := int32((index + 1) * expr.Type.ArrayElement.Bits)

		indexType := types.Info{
			Type:    types.Uint,
			Bits:    32,
			MinBits: 32,
		}
		fromConst, err := ssa.Constant(gen, from, indexType)
		if err != nil {
			return nil, nil, err
		}
		toConst, err := ssa.Constant(gen, to, indexType)
		if err != nil {
			return nil, nil, err
		}
		t := gen.AnonVar(*expr.Type.ArrayElement)
		block.AddInstr(ssa.NewSliceInstr(expr, fromConst, toConst, t))

		return block, []ssa.Variable{t}, nil

	default:
		return nil, nil, ctx.logger.Errorf(ast.Loc,
			"invalid operation: %s[%d] (type %s does not support indexing)",
			ast.Expr, index, expr.Type)
	}
}

// SSA implements the compiler.ast.AST.SSA for variable references.
func (ast *VariableRef) SSA(block *ssa.Block, ctx *Codegen,
	gen *ssa.Generator) (*ssa.Block, []ssa.Variable, error) {

	var b ssa.Binding
	var ok bool

	// Check if package name is bound to variable.
	b, ok = block.Bindings.Get(ast.Name.Package)
	if ok {
		// Selector.
		value := b.Value(block, gen)
		if value.Type.Type != types.Struct {
			return nil, nil, ctx.logger.Errorf(ast.Loc,
				"%s.%s undefined", ast.Name.Package, ast.Name.Name)
		}

		var field *types.StructField
		for _, f := range value.Type.Struct {
			if f.Name == ast.Name.Name {
				field = &f
				break
			}
		}
		if field == nil {
			return nil, nil, ctx.logger.Errorf(ast.Loc,
				"%s.%s undefined (type %s has no field or method %s)",
				ast.Name.Package, ast.Name.Name, value.Type, ast.Name.Name)
		}

		t := gen.AnonVar(types.Info{
			Type:    field.Type.Type,
			Bits:    field.Type.Bits,
			MinBits: field.Type.Bits,
		})

		fromConst, err := ssa.Constant(gen, int32(field.Type.Offset), t.Type)
		if err != nil {
			return nil, nil, err
		}
		toConst, err := ssa.Constant(gen,
			int32(field.Type.Offset+field.Type.Bits), t.Type)
		if err != nil {
			return nil, nil, err
		}

		block.AddInstr(ssa.NewSliceInstr(value, fromConst, toConst, t))
		return block, []ssa.Variable{t}, nil
	}

	if len(ast.Name.Package) > 0 {
		var pkg *Package
		pkg, ok = ctx.Packages[ast.Name.Package]
		if !ok {
			return nil, nil,
				ctx.logger.Errorf(ast.Loc, "package '%s' not found",
					ast.Name.Package)
		}
		b, ok = pkg.Bindings.Get(ast.Name.Name)
	} else {
		b, ok = block.Bindings.Get(ast.Name.Name)
	}
	if !ok {
		return nil, nil, ctx.logger.Errorf(ast.Loc, "undefined variable '%s'",
			ast.Name.String())
	}

	value := b.Value(block, gen)

	// Bind variable with the name it was referenced.
	lValue := value
	lValue.Name = ast.Name.String()
	block.Bindings.Set(lValue, &value)

	if value.Const {
		gen.AddConstant(value)
	}

	return block, []ssa.Variable{value}, nil
}

// SSA implements the compiler.ast.AST.SSA for constant values.
func (ast *Constant) SSA(block *ssa.Block, ctx *Codegen,
	gen *ssa.Generator) (*ssa.Block, []ssa.Variable, error) {

	v, err := ssa.Constant(gen, ast.Value, types.UndefinedInfo)
	if err != nil {
		return nil, nil, err
	}
	gen.AddConstant(v)

	return block, []ssa.Variable{v}, nil
}
