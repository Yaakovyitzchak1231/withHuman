package loggermdc

import (
	"context"
	"log/slog"
)

// Handler decorates another slog.Handler with fields stored in the log call's
// context. It keeps MDC fields at the root even when a logger uses groups.
type Handler struct {
	next   slog.Handler
	groups []string
	bound  []scopedAttrs
}

type scopedAttrs struct {
	groups []string
	attrs  []slog.Attr
}

// NewHandler wraps next with MDC extraction.
func NewHandler(next slog.Handler) *Handler {
	return &Handler{next: next}
}

func (h *Handler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.next.Enabled(ctx, level)
}

func (h *Handler) Handle(ctx context.Context, record slog.Record) error {
	mdc := From(ctx)
	tree := newAttrTree(mdc)
	for _, scoped := range h.bound {
		tree.add(scoped.groups, scoped.attrs)
	}
	record.Attrs(func(attr slog.Attr) bool {
		tree.add(h.groups, []slog.Attr{attr})
		return true
	})

	output := slog.NewRecord(record.Time, record.Level, record.Message, record.PC)
	for _, key := range sortedKeys(mdc) {
		output.AddAttrs(slog.String(string(key), mdc[key]))
	}
	output.AddAttrs(tree.attrs()...)
	return h.next.Handle(ctx, output)
}

func (h *Handler) WithAttrs(attrs []slog.Attr) slog.Handler {
	if len(attrs) == 0 {
		return h
	}
	cloned := h.clone()
	resolved := make([]slog.Attr, 0, len(attrs))
	for _, attr := range attrs {
		resolved = append(resolved, resolveAttr(attr))
	}
	cloned.bound = append(cloned.bound, scopedAttrs{groups: append([]string(nil), h.groups...), attrs: resolved})
	return cloned
}

func (h *Handler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}
	cloned := h.clone()
	cloned.groups = append(cloned.groups, name)
	return cloned
}

func (h *Handler) clone() *Handler {
	return &Handler{
		next:   h.next,
		groups: append([]string(nil), h.groups...),
		bound:  append([]scopedAttrs(nil), h.bound...),
	}
}

type attrTree struct {
	reserved map[string]struct{}
	values   []slog.Attr
	groups   map[string]*attrTree
	order    []string
}

func newAttrTree(mdc Fields) *attrTree {
	reserved := make(map[string]struct{}, len(mdc))
	for key := range mdc {
		reserved[string(key)] = struct{}{}
	}
	return &attrTree{reserved: reserved, groups: make(map[string]*attrTree)}
}

func (t *attrTree) add(groups []string, attrs []slog.Attr) {
	node := t
	for _, group := range groups {
		node = node.group(group)
	}
	for _, attr := range attrs {
		node.addAttr(attr)
	}
}

func (t *attrTree) addAttr(attr slog.Attr) {
	attr = resolveAttr(attr)
	if attr.Equal(slog.Attr{}) {
		return
	}
	if attr.Key != "" {
		if _, reserved := t.reserved[attr.Key]; reserved {
			return
		}
	}
	if attr.Value.Kind() == slog.KindGroup {
		if attr.Key == "" {
			for _, child := range attr.Value.Group() {
				t.addAttr(child)
			}
			return
		}
		group := t.group(attr.Key)
		for _, child := range attr.Value.Group() {
			group.addAttr(child)
		}
		return
	}
	t.values = append(t.values, attr)
}

func (t *attrTree) group(name string) *attrTree {
	if child, ok := t.groups[name]; ok {
		return child
	}
	child := &attrTree{reserved: make(map[string]struct{}), groups: make(map[string]*attrTree)}
	t.groups[name] = child
	t.order = append(t.order, name)
	return child
}

func (t *attrTree) attrs() []slog.Attr {
	attrs := append([]slog.Attr(nil), t.values...)
	for _, name := range t.order {
		children := t.groups[name].attrs()
		if len(children) > 0 {
			attrs = append(attrs, slog.Group(name, attrsToAny(children)...))
		}
	}
	return attrs
}

func resolveAttr(attr slog.Attr) slog.Attr {
	attr.Value = attr.Value.Resolve()
	return attr
}

func attrsToAny(attrs []slog.Attr) []any {
	values := make([]any, len(attrs))
	for index, attr := range attrs {
		values[index] = attr
	}
	return values
}
