package approvalblock

import (
	"fmt"
	"regexp"
	"sort"
	"sync"
)

// Config is the type-specific configuration of a block kind an edition
// registers. Its JSON is the block's wire `config`; the decoder fills a
// fresh value strictly, so unknown fields are refused like everywhere
// else in a document.
type Config interface {
	Validate() error
}

// RoutedConfig is a configuration whose human outcome names an
// escalation path. Kinds that only ever pass a request on need not
// implement it.
type RoutedConfig interface {
	EscalationPath() string
}

// OwnerRoutedConfig is a routed configuration that can also send its human
// outcome to the agent's owner, with its escalation path as the fallback.
type OwnerRoutedConfig interface {
	RoutedConfig
	AskAgentOwner() bool
}

// ReasonedConfig is a configuration carrying the static reason a block's
// decisions record, so the DSL's Reason option reaches it.
type ReasonedConfig interface {
	SetReason(reason string)
}

// Kind is one block type an edition adds to the document grammar: its
// envelope limits and how to make an empty configuration for the decoder.
// The runtime that executes the kind is registered separately, on the
// pipeline's block registry, because execution needs dependencies the
// grammar does not.
type Kind struct {
	Type   BlockType
	Limits TypeLimits
	// New returns a fresh configuration, a pointer the decoder fills.
	New func() Config
}

var kindTypePattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,30}$`)

var kinds = struct {
	sync.RWMutex
	entries map[BlockType]Kind
}{entries: map[BlockType]Kind{}}

// RegisterKind adds a kind to the grammar. It runs once at composition
// time, before any document is decoded. A core type, a duplicate, a
// malformed type name, missing limits, or a nil constructor is refused.
func RegisterKind(kind Kind) error {
	if !kindTypePattern.MatchString(string(kind.Type)) {
		return fmt.Errorf("block kind %q: type must match %s", kind.Type, kindTypePattern)
	}
	if _, core := typeLimits[kind.Type]; core {
		return fmt.Errorf("block kind %q: the type is core's", kind.Type)
	}
	if kind.New == nil {
		return fmt.Errorf("block kind %q: a configuration constructor is required", kind.Type)
	}
	if kind.Limits.DefaultTimeout <= 0 || kind.Limits.MaximumTimeout < kind.Limits.DefaultTimeout ||
		kind.Limits.MinimumAttempts < 1 || kind.Limits.MaximumAttempts < kind.Limits.MinimumAttempts {
		return fmt.Errorf("block kind %q: limits must hold a positive default timeout within the maximum and an attempt range from at least one", kind.Type)
	}
	kinds.Lock()
	defer kinds.Unlock()
	if _, exists := kinds.entries[kind.Type]; exists {
		return fmt.Errorf("block kind %q: already registered", kind.Type)
	}
	kinds.entries[kind.Type] = kind
	return nil
}

// registeredKind finds an edition kind by type.
func registeredKind(blockType BlockType) (Kind, bool) {
	kinds.RLock()
	defer kinds.RUnlock()
	kind, ok := kinds.entries[blockType]
	return kind, ok
}

// RegisteredKinds lists the edition kinds by type.
func RegisteredKinds() []Kind {
	kinds.RLock()
	defer kinds.RUnlock()
	list := make([]Kind, 0, len(kinds.entries))
	for _, kind := range kinds.entries {
		list = append(list, kind)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Type < list[j].Type })
	return list
}

// DefineKind builds a block document of a registered kind around its
// configuration, with the kind's default timeout and one attempt. The
// options then apply as for any block.
func DefineKind(id string, blockType BlockType, config Config, options ...Option) *Definition {
	limits, _ := LimitsFor(blockType)
	definition := &Definition{
		ID: id, Type: blockType, Enabled: true,
		Timeout: limits.DefaultTimeout.String(), MaxAttempts: limits.MinimumAttempts,
		Config: config,
	}
	for _, option := range options {
		if option != nil {
			option(definition)
		}
	}
	return definition
}
