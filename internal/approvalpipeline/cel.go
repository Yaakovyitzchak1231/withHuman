package approvalpipeline

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/google/cel-go/cel"
	"github.com/google/cel-go/common/types"
	"github.com/google/cel-go/common/types/ref"
	"github.com/google/cel-go/common/types/traits"
	"github.com/withHumanAI/withHuman/approvalblock"
)

// ConditionEngineFingerprint versions the condition engine every branch
// and delegate condition compiles with; their runtime fingerprints carry
// it, so an engine change is a runtime change.
const ConditionEngineFingerprint = "cel-go/v0.25.0;withhuman-conditions/v3;limits/v1"

// conditionBudget is the budget key every condition evaluation spends,
// whichever block the condition belongs to.
const conditionBudget approvalblock.BlockType = "condition"

// compiledWhen is one compiled condition envelope with the envelope timeout
// that bounds an evaluation. Branches and delegates share it.
type compiledWhen struct {
	timeout time.Duration
	root    *compiledCondition
}

func compileWhen(condition approvalblock.Condition, timeout time.Duration) (compiledWhen, error) {
	compiler := conditionCompiler{}
	root, err := compiler.compile(condition, false)
	if err != nil {
		return compiledWhen{}, err
	}
	if compiler.expressionBytes > approvalblock.MaximumExpressionBytes {
		return compiledWhen{}, fmt.Errorf("generated expression exceeds %d bytes", approvalblock.MaximumExpressionBytes)
	}
	return compiledWhen{timeout: timeout, root: root}, nil
}

// evaluate spends one condition evaluation from the request's budget and
// runs the condition against the block input, returning the value-free
// evidence even when evaluation fails.
func (when compiledWhen) evaluate(ctx context.Context, input RequestApprovalBlockInput) (bool, BlockEvidence, error) {
	if err := input.Budget.Consume(conditionBudget, approvalblock.MaximumConditionsPerRequest); err != nil {
		return false, BlockEvidence{}, executionError(ErrorResourceLimit, err)
	}
	if ctx == nil {
		return false, BlockEvidence{}, executionError(ErrorInvalidInput, errors.New("context is required"))
	}
	evaluationCtx, cancel := context.WithTimeout(ctx, when.timeout)
	defer cancel()

	root, err := buildSafeInput(input)
	if err != nil {
		return false, BlockEvidence{}, executionError(ErrorInvalidInput, err)
	}
	state := evaluationState{ctx: evaluationCtx, root: root}
	matched, err := state.evaluate(when.root, nil)
	return matched, BlockEvidence{Cost: state.cost, Trace: state.trace}, err
}

type conditionKind uint8

const (
	conditionLeaf conditionKind = iota
	conditionAll
	conditionAny
	conditionSome
	conditionEvery
)

type compiledCondition struct {
	id       string
	kind     conditionKind
	path     []string
	relative bool
	program  cel.Program
	expected any
	children []*compiledCondition
	where    *compiledCondition
	source   string
}

type conditionCompiler struct {
	expressionBytes int
}

func (compiler *conditionCompiler) compile(condition approvalblock.Condition, relative bool) (*compiledCondition, error) {
	node := &compiledCondition{id: condition.ID, relative: relative}
	switch {
	case condition.Operator != "" || condition.Field != "" || condition.Value != nil:
		node.kind = conditionLeaf
		path, err := parsePointer(condition.Field)
		if err != nil {
			return nil, err
		}
		node.path = path
		if condition.Value != nil {
			node.expected, err = decodeJSON(condition.Value)
			if err != nil {
				return nil, fmt.Errorf("condition %q value: %w", condition.ID, err)
			}
		}
		var compiledRegex *regexp.Regexp
		if condition.Operator == approvalblock.Regex {
			pattern, _ := node.expected.(string)
			compiledRegex, err = regexp.Compile(pattern)
			if err != nil {
				return nil, fmt.Errorf("condition %q regex: %w", condition.ID, err)
			}
		}
		node.source = leafExpression(condition.Operator)
		node.program, err = compileLeafProgram(node.source, operatorBinding(condition.Operator, compiledRegex))
		if err != nil {
			return nil, fmt.Errorf("condition %q CEL: %w", condition.ID, err)
		}
		compiler.expressionBytes += len(node.source)
		return node, nil
	case condition.All != nil:
		node.kind = conditionAll
		node.source = "all"
		node.children = make([]*compiledCondition, len(condition.All))
		for index, child := range condition.All {
			compiled, err := compiler.compile(child, relative)
			if err != nil {
				return nil, err
			}
			node.children[index] = compiled
		}
	case condition.Any != nil:
		node.kind = conditionAny
		node.source = "any"
		node.children = make([]*compiledCondition, len(condition.Any))
		for index, child := range condition.Any {
			compiled, err := compiler.compile(child, relative)
			if err != nil {
				return nil, err
			}
			node.children[index] = compiled
		}
	case condition.Some != nil:
		node.kind = conditionSome
		node.source = "some"
		path, err := parsePointer(condition.Some.Field)
		if err != nil {
			return nil, err
		}
		node.path = path
		compiled, err := compiler.compile(condition.Some.Where, true)
		if err != nil {
			return nil, err
		}
		node.where = compiled
	case condition.Every != nil:
		node.kind = conditionEvery
		node.source = "every"
		path, err := parsePointer(condition.Every.Field)
		if err != nil {
			return nil, err
		}
		node.path = path
		compiled, err := compiler.compile(condition.Every.Where, true)
		if err != nil {
			return nil, err
		}
		node.where = compiled
	default:
		return nil, fmt.Errorf("condition %q has no executable shape", condition.ID)
	}
	compiler.expressionBytes += len(node.source)
	return node, nil
}

func leafExpression(operator approvalblock.Operator) string {
	switch operator {
	case approvalblock.Exists:
		return "present"
	case approvalblock.NotExists:
		return "!present"
	default:
		return "present ? wh_eval(value, expected) : false"
	}
}

func compileLeafProgram(expression string, binding func(ref.Val, ref.Val) ref.Val) (cel.Program, error) {
	environment, err := cel.NewEnv(
		cel.ClearMacros(),
		cel.CrossTypeNumericComparisons(true),
		cel.Variable("present", cel.BoolType),
		cel.Variable("value", cel.DynType),
		cel.Variable("expected", cel.DynType),
		cel.Function("wh_eval", cel.Overload(
			"withhuman_eval_dyn_dyn",
			[]*cel.Type{cel.DynType, cel.DynType},
			cel.BoolType,
			cel.BinaryBinding(binding),
		)),
	)
	if err != nil {
		return nil, err
	}
	ast, issues := environment.Compile(expression)
	if issues != nil && issues.Err() != nil {
		return nil, issues.Err()
	}
	if ast.OutputType() != cel.BoolType {
		return nil, fmt.Errorf("expression result must be bool, got %s", ast.OutputType())
	}
	return environment.Program(
		ast,
		cel.CostTracking(operationCostEstimator{}),
		cel.CostLimit(approvalblock.MaximumEvaluationCost),
	)
}

type operationCostEstimator struct{}

func (operationCostEstimator) CallCost(function, overloadID string, args []ref.Val, _ ref.Val) *uint64 {
	if function != "wh_eval" || overloadID != "withhuman_eval_dyn_dyn" {
		return nil
	}
	cost := uint64(1)
	for _, argument := range args {
		cost += valueCost(argument)
	}
	return &cost
}

func valueCost(value ref.Val) uint64 {
	switch typed := value.(type) {
	case types.String:
		return uint64(len(typed))
	case traits.Sizer:
		size := typed.Size()
		if integer, ok := size.(types.Int); ok && integer > 0 {
			return uint64(integer)
		}
	}
	return 1
}

func operatorBinding(operator approvalblock.Operator, compiledRegex *regexp.Regexp) func(ref.Val, ref.Val) ref.Val {
	return func(value, expected ref.Val) ref.Val {
		switch operator {
		case approvalblock.Equals:
			return jsonEqual(value, expected)
		case approvalblock.NotEquals:
			return negate(jsonEqual(value, expected))
		case approvalblock.IsNull:
			return types.Bool(value == types.NullValue)
		case approvalblock.IsNotNull:
			return types.Bool(value != types.NullValue)
		case approvalblock.In:
			return stringIn(value, expected, false)
		case approvalblock.NotIn:
			return stringIn(value, expected, true)
		case approvalblock.Contains:
			return contains(value, expected, false)
		case approvalblock.NotContains:
			return contains(value, expected, true)
		case approvalblock.StartsWith:
			return stringPrefixAny(value, expected)
		case approvalblock.EndsWith:
			return stringAffix(value, expected, strings.HasSuffix)
		case approvalblock.Regex:
			text, ok := value.(types.String)
			if !ok {
				return types.NewErr("regex target must be a string")
			}
			return types.Bool(compiledRegex.MatchString(string(text)))
		case approvalblock.GreaterThan:
			return compareNumbers(value, expected, 1)
		case approvalblock.GreaterThanOrEqual:
			return compareNumbers(value, expected, 0, 1)
		case approvalblock.LessThan:
			return compareNumbers(value, expected, -1)
		case approvalblock.LessThanOrEqual:
			return compareNumbers(value, expected, -1, 0)
		case approvalblock.IsTrue:
			return booleanIs(value, true)
		case approvalblock.IsFalse:
			return booleanIs(value, false)
		case approvalblock.ContainsAny:
			return containsSet(value, expected, false)
		case approvalblock.ContainsAll:
			return containsSet(value, expected, true)
		case approvalblock.IsEmpty:
			return listEmpty(value, true)
		case approvalblock.IsNotEmpty:
			return listEmpty(value, false)
		default:
			return types.NewErr("unsupported generated operation")
		}
	}
}

func jsonEqual(left, right ref.Val) ref.Val {
	equal := left.Equal(right)
	if types.IsError(equal) || types.IsUnknown(equal) {
		return equal
	}
	boolean, ok := equal.(types.Bool)
	if !ok {
		return types.NewErr("equality returned a non-boolean result")
	}
	return boolean
}

func negate(value ref.Val) ref.Val {
	boolean, ok := value.(types.Bool)
	if !ok {
		return value
	}
	return !boolean
}

func stringIn(value, expected ref.Val, invert bool) ref.Val {
	if _, ok := value.(types.String); !ok {
		return types.NewErr("in target must be a string")
	}
	list, ok := expected.(traits.Lister)
	if !ok {
		return types.NewErr("in comparison must be a list")
	}
	return maybeInvert(list.Contains(value), invert)
}

func contains(value, expected ref.Val, invert bool) ref.Val {
	var result ref.Val
	switch typed := value.(type) {
	case types.String:
		comparison, ok := expected.(types.String)
		if !ok {
			return types.NewErr("string contains comparison must be a string")
		}
		result = types.Bool(strings.Contains(string(typed), string(comparison)))
	case traits.Lister:
		result = typed.Contains(expected)
	default:
		return types.NewErr("contains target must be a string or array")
	}
	return maybeInvert(result, invert)
}

// stringPrefixAny is starts_with: the string target begins with at least one
// of the listed prefixes, so one rule covers a whole family of tool names.
func stringPrefixAny(value, expected ref.Val) ref.Val {
	text, ok := value.(types.String)
	if !ok {
		return types.NewErr("starts_with target must be a string")
	}
	prefixes, ok := expected.(traits.Lister)
	if !ok {
		return types.NewErr("starts_with comparison must be a list")
	}
	iterator := prefixes.Iterator()
	for iterator.HasNext() == types.True {
		prefix, ok := iterator.Next().(types.String)
		if !ok {
			return types.NewErr("starts_with prefix must be a string")
		}
		if strings.HasPrefix(string(text), string(prefix)) {
			return types.True
		}
	}
	return types.False
}

func stringAffix(value, expected ref.Val, compare func(string, string) bool) ref.Val {
	text, textOK := value.(types.String)
	affix, affixOK := expected.(types.String)
	if !textOK || !affixOK {
		return types.NewErr("string operator requires string operands")
	}
	return types.Bool(compare(string(text), string(affix)))
}

func compareNumbers(value, expected ref.Val, accepted ...types.Int) ref.Val {
	comparer, ok := value.(traits.Comparer)
	if !ok || !isNumber(value) || !isNumber(expected) {
		return types.NewErr("numeric operator requires numeric operands")
	}
	comparison := comparer.Compare(expected)
	if types.IsError(comparison) || types.IsUnknown(comparison) {
		return comparison
	}
	for _, candidate := range accepted {
		if comparison == candidate {
			return types.True
		}
	}
	return types.False
}

func isNumber(value ref.Val) bool {
	switch value.(type) {
	case types.Int, types.Uint, types.Double:
		return true
	default:
		return false
	}
}

func booleanIs(value ref.Val, expected bool) ref.Val {
	boolean, ok := value.(types.Bool)
	if !ok {
		return types.NewErr("boolean operator requires a boolean target")
	}
	return types.Bool(bool(boolean) == expected)
}

// containsSet is contains_any and contains_all: element membership for an
// array target and substring containment for a string target, so a rule on
// a shell command reads the way its author wrote it.
func containsSet(value, expected ref.Val, requireAll bool) ref.Val {
	comparisons, ok := expected.(traits.Lister)
	if !ok {
		return types.NewErr("containment comparison must be a list")
	}
	var contains func(ref.Val) ref.Val
	switch target := value.(type) {
	case types.String:
		contains = func(candidate ref.Val) ref.Val {
			text, ok := candidate.(types.String)
			if !ok {
				return types.NewErr("string containment comparison must be a string")
			}
			return types.Bool(strings.Contains(string(target), string(text)))
		}
	case traits.Lister:
		contains = target.Contains
	default:
		return types.NewErr("containment target must be a string or array")
	}
	iterator := comparisons.Iterator()
	found := false
	for iterator.HasNext() == types.True {
		found = true
		contained := contains(iterator.Next())
		boolean, ok := contained.(types.Bool)
		if !ok {
			return contained
		}
		if requireAll && !bool(boolean) {
			return types.False
		}
		if !requireAll && bool(boolean) {
			return types.True
		}
	}
	if requireAll {
		return types.Bool(found)
	}
	return types.False
}

func listEmpty(value ref.Val, empty bool) ref.Val {
	list, ok := value.(traits.Lister)
	if !ok {
		return types.NewErr("empty operator requires an array target")
	}
	size, ok := list.Size().(types.Int)
	if !ok {
		return types.NewErr("array size was not an integer")
	}
	return types.Bool((size == 0) == empty)
}

func maybeInvert(value ref.Val, invert bool) ref.Val {
	if !invert {
		return value
	}
	return negate(value)
}

type evaluationState struct {
	ctx        context.Context
	root       map[string]any
	cost       uint64
	iterations int
	trace      []NodeTrace
}

func (state *evaluationState) evaluate(node *compiledCondition, current any) (bool, error) {
	if err := state.ctx.Err(); err != nil {
		return false, executionError(ErrorTimeout, err)
	}
	state.cost++
	if state.cost > approvalblock.MaximumEvaluationCost {
		return false, executionError(ErrorCostLimit, errors.New("condition traversal cost exceeded"))
	}

	var matched bool
	var err error
	switch node.kind {
	case conditionLeaf:
		matched, err = state.evaluateLeaf(node, current)
	case conditionAll:
		matched = true
		for _, child := range node.children {
			var childMatched bool
			childMatched, err = state.evaluate(child, current)
			if err != nil || !childMatched {
				matched = false
				break
			}
		}
	case conditionAny:
		for _, child := range node.children {
			var childMatched bool
			childMatched, err = state.evaluate(child, current)
			if err != nil || childMatched {
				matched = childMatched
				break
			}
		}
	case conditionSome, conditionEvery:
		matched, err = state.evaluateQuantifier(node, current)
	default:
		err = executionError(ErrorEvaluation, errors.New("unknown compiled condition kind"))
	}
	state.trace = append(state.trace, NodeTrace{NodeID: node.id, Matched: matched, Error: err != nil})
	return matched, err
}

func (state *evaluationState) evaluateLeaf(node *compiledCondition, current any) (bool, error) {
	value, present, err := state.resolve(node, current)
	if err != nil {
		return false, executionError(ErrorEvaluation, err)
	}
	result, details, err := node.program.ContextEval(state.ctx, map[string]any{
		"present":  present,
		"value":    value,
		"expected": node.expected,
	})
	if details != nil && details.ActualCost() != nil {
		state.cost += *details.ActualCost()
	}
	if state.cost > approvalblock.MaximumEvaluationCost {
		return false, executionError(ErrorCostLimit, errors.New("CEL cost exceeded"))
	}
	if err != nil {
		if strings.Contains(err.Error(), "cost limit exceeded") {
			return false, executionError(ErrorCostLimit, err)
		}
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
			return false, executionError(ErrorTimeout, err)
		}
		return false, executionError(ErrorEvaluation, err)
	}
	boolean, ok := result.(types.Bool)
	if !ok {
		return false, executionError(ErrorEvaluation, errors.New("CEL result was not boolean"))
	}
	return bool(boolean), nil
}

func (state *evaluationState) evaluateQuantifier(node *compiledCondition, current any) (bool, error) {
	value, present, err := state.resolve(node, current)
	if err != nil {
		return false, executionError(ErrorEvaluation, err)
	}
	if !present {
		return false, nil
	}
	values, ok := value.([]any)
	if !ok {
		return false, executionError(ErrorEvaluation, errors.New("quantifier target was not an array"))
	}
	if len(values) == 0 {
		return false, nil
	}
	if state.iterations+len(values) > approvalblock.MaximumComprehensionEntries {
		return false, executionError(ErrorResourceLimit, errors.New("comprehension entry limit exceeded"))
	}
	state.iterations += len(values)

	if node.kind == conditionSome {
		for _, value := range values {
			matched, err := state.evaluate(node.where, value)
			if err != nil || matched {
				return matched, err
			}
		}
		return false, nil
	}
	for _, value := range values {
		matched, err := state.evaluate(node.where, value)
		if err != nil || !matched {
			return false, err
		}
	}
	return true, nil
}

func (state *evaluationState) resolve(node *compiledCondition, current any) (any, bool, error) {
	if node.relative {
		return resolvePointer(current, node.path)
	}
	return resolvePointer(state.root, node.path)
}

func buildSafeInput(input RequestApprovalBlockInput) (map[string]any, error) {
	inputBytes := len(input.Request.Tool) + len(input.Request.Server) + len(input.Request.Arguments) + len(input.Agent.Slug) + len(input.Agent.Name)
	if inputBytes > approvalblock.MaximumRequestInputBytes {
		return nil, fmt.Errorf("request input exceeds %d bytes", approvalblock.MaximumRequestInputBytes)
	}
	if input.Request.Tool == "" || input.Agent.Slug == "" || input.Agent.Name == "" {
		return nil, errors.New("request tool and canonical agent fields are required")
	}
	arguments, err := decodeJSON(input.Request.Arguments)
	if err != nil {
		return nil, fmt.Errorf("request arguments: %w", err)
	}
	argumentObject, ok := arguments.(map[string]any)
	if !ok {
		return nil, errors.New("request arguments must be a JSON object")
	}
	previous, err := nestPreviousMetadata(input.PreviousMetadata)
	if err != nil {
		return nil, err
	}
	requestObject := map[string]any{"tool": input.Request.Tool, "arguments": argumentObject}
	if input.Request.Server != "" {
		// Absent for built-in tools, so a not_exists condition names them.
		requestObject["server"] = input.Request.Server
	}
	return map[string]any{
		"request":           requestObject,
		"agent":             map[string]any{"slug": input.Agent.Slug, "name": input.Agent.Name},
		"previous_metadata": previous,
	}, nil
}

// nestPreviousMetadata exposes earlier blocks' metadata to conditions as
// /previous_metadata/<scope>/<block_key>/...; a block that did not run or
// errored is simply absent, so only not_exists matches it.
func nestPreviousMetadata(flat map[string]json.RawMessage) (map[string]any, error) {
	previous := make(map[string]any)
	for key, raw := range flat {
		scope, blockKey, found := strings.Cut(key, "/")
		if !found || scope == "" || blockKey == "" {
			continue
		}
		value, err := decodeJSON(raw)
		if err != nil {
			return nil, fmt.Errorf("previous metadata %q: %w", key, err)
		}
		scoped, _ := previous[scope].(map[string]any)
		if scoped == nil {
			scoped = make(map[string]any)
			previous[scope] = scoped
		}
		scoped[blockKey] = value
	}
	return previous, nil
}

func resolvePointer(value any, path []string) (any, bool, error) {
	current := value
	for _, token := range path {
		switch typed := current.(type) {
		case map[string]any:
			var exists bool
			current, exists = typed[token]
			if !exists {
				return nil, false, nil
			}
		case []any:
			if token == "" || (len(token) > 1 && token[0] == '0') {
				return nil, false, nil
			}
			index, err := strconv.Atoi(token)
			if err != nil || index < 0 || index >= len(typed) {
				return nil, false, nil
			}
			current = typed[index]
		default:
			return nil, false, errors.New("field path traversed a scalar value")
		}
	}
	return current, true, nil
}

func parsePointer(path string) ([]string, error) {
	if path == "" {
		return nil, nil
	}
	if !strings.HasPrefix(path, "/") {
		return nil, fmt.Errorf("path %q must be a JSON Pointer", path)
	}
	parts := strings.Split(path[1:], "/")
	for index, part := range parts {
		var decoded strings.Builder
		for offset := 0; offset < len(part); offset++ {
			if part[offset] != '~' {
				decoded.WriteByte(part[offset])
				continue
			}
			if offset+1 >= len(part) || (part[offset+1] != '0' && part[offset+1] != '1') {
				return nil, fmt.Errorf("path %q contains an invalid JSON Pointer escape", path)
			}
			offset++
			if part[offset] == '0' {
				decoded.WriteByte('~')
			} else {
				decoded.WriteByte('/')
			}
		}
		parts[index] = decoded.String()
	}
	return parts, nil
}

func decodeJSON(raw []byte) (any, error) {
	if len(raw) == 0 {
		return nil, errors.New("JSON value is required")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	value, err := decodeJSONValue(decoder)
	if err != nil {
		return nil, err
	}
	if token, err := decoder.Token(); !errors.Is(err, io.EOF) {
		if err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("unexpected trailing JSON token %v", token)
	}
	return value, nil
}

func decodeJSONValue(decoder *json.Decoder) (any, error) {
	token, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	delimiter, isDelimiter := token.(json.Delim)
	if !isDelimiter {
		if number, ok := token.(json.Number); ok {
			return convertNumber(number)
		}
		return token, nil
	}
	switch delimiter {
	case '{':
		object := make(map[string]any)
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return nil, err
			}
			key, ok := keyToken.(string)
			if !ok {
				return nil, errors.New("JSON object key was not a string")
			}
			if _, exists := object[key]; exists {
				return nil, fmt.Errorf("duplicate JSON object key %q", key)
			}
			value, err := decodeJSONValue(decoder)
			if err != nil {
				return nil, err
			}
			object[key] = value
		}
		if closing, err := decoder.Token(); err != nil || closing != json.Delim('}') {
			return nil, errors.New("JSON object was not terminated")
		}
		return object, nil
	case '[':
		array := make([]any, 0)
		for decoder.More() {
			value, err := decodeJSONValue(decoder)
			if err != nil {
				return nil, err
			}
			array = append(array, value)
		}
		if closing, err := decoder.Token(); err != nil || closing != json.Delim(']') {
			return nil, errors.New("JSON array was not terminated")
		}
		return array, nil
	default:
		return nil, fmt.Errorf("unexpected JSON delimiter %q", delimiter)
	}
}

func convertNumber(number json.Number) (any, error) {
	text := number.String()
	if !strings.ContainsAny(text, ".eE") {
		if integer, err := strconv.ParseInt(text, 10, 64); err == nil {
			return integer, nil
		}
		if unsigned, err := strconv.ParseUint(text, 10, 64); err == nil {
			return unsigned, nil
		}
	}
	floating, err := strconv.ParseFloat(text, 64)
	if err != nil {
		return nil, fmt.Errorf("number %q is outside the supported CEL range", text)
	}
	return floating, nil
}

func cloneDefinition(definition approvalblock.Definition) (approvalblock.Definition, error) {
	encoded, err := json.Marshal(definition)
	if err != nil {
		return approvalblock.Definition{}, err
	}
	var clone approvalblock.Definition
	if err := json.Unmarshal(encoded, &clone); err != nil {
		return approvalblock.Definition{}, err
	}
	return clone, nil
}

func blockOutcome(outcome approvalblock.Outcome) BlockOutcome {
	switch outcome {
	case approvalblock.Approve:
		return BlockApprove
	case approvalblock.Deny:
		return BlockDeny
	case approvalblock.Human:
		return BlockHuman
	case approvalblock.Next:
		return BlockNext
	default:
		return ""
	}
}
