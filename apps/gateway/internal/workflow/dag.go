package workflow

import (
	"fmt"

	"github.com/google/cel-go/cel"
)

// topoSort returns the indices of def.Steps in topological execution order.
// Steps with empty DependsOn depend on the immediately preceding step, which
// preserves backward-compatible linear semantics.
//
// Returns ErrInvalidDef if the dependency graph contains a cycle or references
// an unknown step ID.
func topoSort(def Definition) ([]int, error) {
	n := len(def.Steps)
	idToIdx := make(map[string]int, n)
	for i, s := range def.Steps {
		idToIdx[s.ID] = i
	}

	// Build adjacency: adj[i] = list of steps that depend on step i.
	// We model the reverse: depOf[i] = indices that step i depends on.
	depOf := make([][]int, n)
	for i, s := range def.Steps {
		if len(s.DependsOn) == 0 && i > 0 {
			// Legacy linear: step i depends on step i-1.
			depOf[i] = []int{i - 1}
			continue
		}
		for _, depID := range s.DependsOn {
			j, ok := idToIdx[depID]
			if !ok {
				return nil, fmt.Errorf("%w: step %q depends on unknown step %q", ErrInvalidDef, s.ID, depID)
			}
			if j == i {
				return nil, fmt.Errorf("%w: step %q depends on itself", ErrInvalidDef, s.ID)
			}
			depOf[i] = append(depOf[i], j)
		}
	}

	// Kahn's algorithm.
	inDegree := make([]int, n)
	for i := range depOf {
		inDegree[i] += len(depOf[i])
	}

	// Build reverse: for each step j, which steps i have j as a dependency.
	dependents := make([][]int, n)
	for i, deps := range depOf {
		for _, j := range deps {
			dependents[j] = append(dependents[j], i)
		}
	}

	queue := make([]int, 0, n)
	for i, d := range inDegree {
		if d == 0 {
			queue = append(queue, i)
		}
	}

	order := make([]int, 0, n)
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		order = append(order, cur)
		for _, dep := range dependents[cur] {
			inDegree[dep]--
			if inDegree[dep] == 0 {
				queue = append(queue, dep)
			}
		}
	}

	if len(order) != n {
		return nil, fmt.Errorf("%w: dependency graph contains a cycle", ErrInvalidDef)
	}
	return order, nil
}

// celEnv is the shared CEL environment for when: expression evaluation.
var celEnv *cel.Env

func init() {
	var err error
	celEnv, err = cel.NewEnv(
		cel.Variable("steps", cel.MapType(cel.StringType, cel.DynType)),
		cel.Variable("run_id", cel.StringType),
	)
	if err != nil {
		// init-time failure should never happen with the above config.
		panic("workflow: failed to init CEL environment: " + err.Error())
	}
}

// evalWhen evaluates a CEL when: expression against the current run state.
// Returns true if the step should be dispatched, false if it should be skipped.
// An empty expression always returns true.
func evalWhen(expr string, run *Run, def Definition) (bool, error) {
	if expr == "" {
		return true, nil
	}

	// Build the steps variable: map[stepID]{state, error, job_id}.
	stepsVar := make(map[string]any, len(run.Steps))
	for i, sr := range run.Steps {
		stepID := sr.StepID
		if i < len(def.Steps) {
			stepID = def.Steps[i].ID
		}
		stepsVar[stepID] = map[string]any{
			"state":   string(sr.State),
			"error":   sr.Error,
			"job_id":  sr.JobID,
			"retries": sr.Retries,
		}
	}

	ast, issues := celEnv.Compile(expr)
	if issues != nil && issues.Err() != nil {
		return false, fmt.Errorf("workflow: CEL compile error in when expression %q: %w", expr, issues.Err())
	}
	prog, err := celEnv.Program(ast)
	if err != nil {
		return false, fmt.Errorf("workflow: CEL program error: %w", err)
	}
	out, _, err := prog.Eval(map[string]any{
		"steps":  stepsVar,
		"run_id": run.ID,
	})
	if err != nil {
		return false, fmt.Errorf("workflow: CEL eval error in when expression %q: %w", expr, err)
	}

	result, ok := out.Value().(bool)
	if !ok {
		return false, fmt.Errorf("workflow: when expression %q must evaluate to bool, got %T", expr, out.Value())
	}
	return result, nil
}
