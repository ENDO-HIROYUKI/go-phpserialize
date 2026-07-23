package phpserialize

import (
	"reflect"
	"sort"
	"strings"
)

type structField struct {
	name      string
	index     []int
	typ       reflect.Type
	tagged    bool
	omitEmpty bool
	planIdx   int
}

// structFields は t のエンコード / デコード対象フィールドを返す。
// 匿名 (埋め込み) struct のフィールドは encoding/json に準じてフラット化する:
// 浅い方が優先、同じ深さではタグ付きが優先、それでも競合するものは除外。
func structFields(t reflect.Type) []structField {
	type scan struct {
		typ   reflect.Type
		index []int
	}
	current := []scan{{typ: t}}
	visited := map[reflect.Type]bool{}
	type candidate struct {
		structField
		depth int
	}
	var cands []candidate

	for len(current) > 0 {
		var next []scan
		for _, sc := range current {
			if visited[sc.typ] {
				continue
			}
			visited[sc.typ] = true
			for i := 0; i < sc.typ.NumField(); i++ {
				sf := sc.typ.Field(i)
				tag := sf.Tag.Get("php")
				if tag == "-" {
					continue
				}
				name, opts := parseTag(tag)
				index := make([]int, 0, len(sc.index)+1)
				index = append(append(index, sc.index...), i)

				if sf.Anonymous {
					ft := sf.Type
					if ft.Kind() == reflect.Pointer {
						ft = ft.Elem()
					}
					// 非公開の埋め込みは (フィールドへ到達できないため) 全体をスキップ
					if !sf.IsExported() {
						continue
					}
					if ft.Kind() == reflect.Struct && name == "" {
						next = append(next, scan{typ: ft, index: index})
						continue
					}
				}
				if !sf.IsExported() {
					continue
				}
				if name == "" {
					name = sf.Name
				}
				cands = append(cands, candidate{
					structField: structField{
						name:      name,
						index:     index,
						typ:       sf.Type,
						tagged:    name != sf.Name || tag != "",
						omitEmpty: opts.contains("omitempty"),
					},
					depth: len(index),
				})
			}
		}
		current = next
	}

	// 名前ごとに優先解決
	byName := map[string][]candidate{}
	order := []string{}
	for _, c := range cands {
		if _, ok := byName[c.name]; !ok {
			order = append(order, c.name)
		}
		byName[c.name] = append(byName[c.name], c)
	}
	var out []structField
	for _, name := range order {
		group := byName[name]
		minDepth := group[0].depth
		for _, c := range group {
			if c.depth < minDepth {
				minDepth = c.depth
			}
		}
		var atMin []candidate
		for _, c := range group {
			if c.depth == minDepth {
				atMin = append(atMin, c)
			}
		}
		if len(atMin) == 1 {
			out = append(out, atMin[0].structField)
			continue
		}
		var tagged []candidate
		for _, c := range atMin {
			if c.tagged {
				tagged = append(tagged, c)
			}
		}
		if len(tagged) == 1 {
			out = append(out, tagged[0].structField)
		}
		// 解決できない競合はフィールドごと除外 (encoding/json と同じ)
	}

	// 出力順を安定させる (宣言順 = index の辞書順)
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i].index, out[j].index
		for k := 0; k < len(a) && k < len(b); k++ {
			if a[k] != b[k] {
				return a[k] < b[k]
			}
		}
		return len(a) < len(b)
	})
	return out
}

type tagOptions string

func parseTag(tag string) (string, tagOptions) {
	name, opts, _ := strings.Cut(tag, ",")
	return name, tagOptions(opts)
}

func (o tagOptions) contains(name string) bool {
	s := string(o)
	for s != "" {
		var part string
		part, s, _ = strings.Cut(s, ",")
		if part == name {
			return true
		}
	}
	return false
}
