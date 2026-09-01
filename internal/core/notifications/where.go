package notifications

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/cel-go/cel"
	"github.com/google/cel-go/common/types"
	"github.com/hbaldwin98/control-center/internal/core/events"
)

const (
	celCostLimit   = 10_000
	celEvalTimeout = 50 * time.Millisecond
	celMaxPayload  = 64 << 10
)

func newCELEnv() (*cel.Env, error) {
	return cel.NewEnv(
		cel.Variable("event", cel.MapType(cel.StringType, cel.DynType)),
		cel.EagerlyValidateDeclarations(true),
		cel.DefaultUTCTimeZone(true),
	)
}

func compileWhere(env *cel.Env, expr string) (cel.Program, error) {
	if expr == "" {
		return nil, nil
	}
	ast, iss := env.Compile(expr)
	if iss != nil && iss.Err() != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidWhere, iss.Err())
	}
	if ast.OutputType() != cel.BoolType {
		return nil, fmt.Errorf("%w: expression must be boolean", ErrInvalidWhere)
	}
	prg, err := env.Program(ast, cel.CostLimit(celCostLimit))
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidWhere, err)
	}
	return prg, nil
}

func evalWhere(prg cel.Program, e events.Event, payload any) (bool, error) {
	if prg == nil {
		return true, nil
	}
	if len(e.Payload) > celMaxPayload {
		return false, fmt.Errorf("payload exceeds CEL input limit")
	}
	ctx, cancel := context.WithTimeout(context.Background(), celEvalTimeout)
	defer cancel()
	out, _, err := prg.ContextEval(ctx, map[string]any{
		"event": map[string]any{
			"id":      e.ID,
			"type":    e.Type,
			"source":  e.Source,
			"subject": e.Subject,
			"payload": payload,
		},
	})
	if err != nil {
		return false, err
	}
	b, ok := out.(types.Bool)
	if !ok {
		return false, fmt.Errorf("where result is not boolean")
	}
	return bool(b), nil
}

func decodePayload(raw json.RawMessage) any {
	if len(raw) == 0 {
		return nil
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil
	}
	return v
}
