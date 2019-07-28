// Copyright 2016 PingCAP, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// See the License for the specific language governing permissions and
// limitations under the License.

package aggregation

import (
	"bytes"

	"github.com/juju/errors"
	"github.com/pingcap/tidb/ast"
	"github.com/pingcap/tidb/expression"
	"github.com/pingcap/tidb/sessionctx/stmtctx"
	"github.com/pingcap/tidb/types"
	tipb "github.com/pingcap/tipb/go-tipb"
)

// Aggregation stands for aggregate functions.
type Aggregation interface {
	// Update during executing.
	Update(ctx *AggEvaluateContext, sc *stmtctx.StatementContext, row types.Row) error

	// GetPartialResult will called by coprocessor to get partial results. For avg function, partial results will return
	// sum and count values at the same time.
	GetPartialResult(ctx *AggEvaluateContext) []types.Datum

	// GetResult will be called when all data have been processed.
	GetResult(ctx *AggEvaluateContext) types.Datum

	// Create a new AggEvaluateContext for the aggregation function.
	CreateContext() *AggEvaluateContext
}

// NewDistAggFunc creates new Aggregate function for mock tikv.
func NewDistAggFunc(expr *tipb.Expr, fieldTps []*types.FieldType, sc *stmtctx.StatementContext) (Aggregation, error) {
	args := make([]expression.Expression, 0, len(expr.Children))
	for _, child := range expr.Children {
		arg, err := expression.PBToExpr(child, fieldTps, sc)
		if err != nil {
			return nil, errors.Trace(err)
		}
		args = append(args, arg)
	}
	switch expr.Tp {
	case tipb.ExprType_Sum:
		return &sumFunction{aggFunction: newAggFunc(ast.AggFuncSum, args, false)}, nil
	case tipb.ExprType_Count:
		return &countFunction{aggFunction: newAggFunc(ast.AggFuncCount, args, false)}, nil
	case tipb.ExprType_Avg:
		return &avgFunction{aggFunction: newAggFunc(ast.AggFuncAvg, args, false)}, nil
	case tipb.ExprType_GroupConcat:
		return &concatFunction{aggFunction: newAggFunc(ast.AggFuncGroupConcat, args, false)}, nil
	case tipb.ExprType_Max:
		return &maxMinFunction{aggFunction: newAggFunc(ast.AggFuncMax, args, false), isMax: true}, nil
	case tipb.ExprType_Min:
		return &maxMinFunction{aggFunction: newAggFunc(ast.AggFuncMin, args, false)}, nil
	case tipb.ExprType_First:
		return &firstRowFunction{aggFunction: newAggFunc(ast.AggFuncFirstRow, args, false)}, nil
	case tipb.ExprType_Agg_BitOr:
		return &bitOrFunction{aggFunction: newAggFunc(ast.AggFuncBitOr, args, false)}, nil
	case tipb.ExprType_Agg_BitXor:
		return &bitXorFunction{aggFunction: newAggFunc(ast.AggFuncBitXor, args, false)}, nil
	case tipb.ExprType_Agg_BitAnd:
		return &bitAndFunction{aggFunction: newAggFunc(ast.AggFuncBitAnd, args, false)}, nil
	}
	return nil, errors.Errorf("Unknown aggregate function type %v", expr.Tp)
}

// AggEvaluateContext is used to store intermediate result when calculating aggregate functions.
type AggEvaluateContext struct {
	DistinctChecker *distinctChecker
	Count           int64
	Value           types.Datum
	Buffer          *bytes.Buffer // Buffer is used for group_concat.
	GotFirstRow     bool          // It will check if the agg has met the first row key.
}

// AggFunctionMode stands for the aggregation function's mode.
type AggFunctionMode int

const (
	// CompleteMode function accepts origin data.
	CompleteMode AggFunctionMode = iota
	// FinalMode function accepts partial data.
	FinalMode
)

type aggFunction struct {
	*AggFuncDesc
}

func newAggFunc(funcName string, args []expression.Expression, hasDistinct bool) aggFunction {
	return aggFunction{AggFuncDesc: &AggFuncDesc{
		Name:        funcName,
		Args:        args,
		HasDistinct: hasDistinct,
	}}
}

// CreateContext implements Aggregation interface.
func (af *aggFunction) CreateContext() *AggEvaluateContext {
	ctx := &AggEvaluateContext{}
	if af.HasDistinct {
		ctx.DistinctChecker = createDistinctChecker()
	}
	return ctx
}

func (af *aggFunction) updateSum(ctx *AggEvaluateContext, sc *stmtctx.StatementContext, row types.Row) error {
	a := af.Args[0]
	value, err := a.Eval(row)
	if err != nil {
		return errors.Trace(err)
	}
	if value.IsNull() {
		return nil
	}
	if af.HasDistinct {
		d, err1 := ctx.DistinctChecker.Check(sc, []types.Datum{value})
		if err1 != nil {
			return errors.Trace(err1)
		}
		if !d {
			return nil
		}
	}
	ctx.Value, err = calculateSum(sc, ctx.Value, value)
	if err != nil {
		return errors.Trace(err)
	}
	ctx.Count++
	return nil
}