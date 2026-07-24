package phpserialize

import (
	"fmt"
	"math"
	"reflect"
	"strconv"
	"strings"
	"sync"
)

type decFunc func(s *decodeState, v reflect.Value) error

var decPlans sync.Map // reflect.Type -> decFunc

var unmarshalerType = reflect.TypeFor[Unmarshaler]()

// 巨大キーによる過大なメモリ確保を防ぐ。
const maxSparseArrayKey = (1 << 20) - 1

// decPlanFor は型ごとのデコード関数を返す (初回にコンパイルしてキャッシュ)。
// 再帰型は一時的な間接参照を挟んで解決する (encoding/json と同じ手法)。
func decPlanFor(t reflect.Type) (decFunc, error) {
	if f, ok := decPlans.Load(t); ok {
		return f.(decFunc), nil
	}
	var (
		wg sync.WaitGroup
		f  decFunc
	)
	wg.Add(1)
	indirect := decFunc(func(s *decodeState, v reflect.Value) error {
		wg.Wait()
		return f(s, v)
	})
	if actual, loaded := decPlans.LoadOrStore(t, indirect); loaded {
		return actual.(decFunc), nil
	}
	compiled, err := compileDec(t)
	if err != nil {
		compiled = func(*decodeState, reflect.Value) error { return err }
	}
	f = compiled
	wg.Done()
	decPlans.Store(t, compiled)
	return compiled, err
}

func compileDec(t reflect.Type) (decFunc, error) {
	// Unmarshaler が最優先 (N; の扱いも実装側に委ねるため withNull より前)
	if reflect.PointerTo(t).Implements(unmarshalerType) {
		return decodeViaUnmarshaler, nil
	}
	switch t.Kind() {
	case reflect.Bool:
		return withNull(decodeBool), nil
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return withNull(decodeInt), nil
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return withNull(decodeUint), nil
	case reflect.Float32, reflect.Float64:
		return withNull(decodeFloat), nil
	case reflect.String:
		return withNull(decodeString), nil
	case reflect.Slice:
		if t.Elem().Kind() == reflect.Uint8 {
			return withNull(decodeByteSlice), nil
		}
		return compileSliceDec(t)
	case reflect.Array:
		return compileArrayDec(t)
	case reflect.Map:
		return compileMapDec(t)
	case reflect.Struct:
		return compileStructDec(t)
	case reflect.Interface:
		if t.NumMethod() != 0 {
			return nil, fmt.Errorf("phpserialize: cannot decode into non-empty interface %s", t)
		}
		return withNull(decodeAnyValue), nil
	case reflect.Pointer:
		return compilePointerDec(t)
	}
	return nil, fmt.Errorf("phpserialize: unsupported decode target type %s", t)
}

// withNull は先頭が N; のとき対象をゼロ値にする共通処理を挟む。
// (PHP の null はどの Go 型に対してもゼロ値として扱う。)
func withNull(f decFunc) decFunc {
	return func(s *decodeState, v reflect.Value) error {
		if tag, err := s.peek(); err != nil {
			return err
		} else if tag == 'N' {
			if err := s.parseNull(); err != nil {
				return err
			}
			v.SetZero()
			return nil
		}
		return f(s, v)
	}
}

func (s *decodeState) typeErr(off int, tag byte, t reflect.Type) error {
	return &TypeError{Offset: off, PHPType: phpTypeName(tag), GoType: t}
}

func decodeViaUnmarshaler(s *decodeState, v reflect.Value) error {
	start := s.off
	if err := s.skipValue(); err != nil {
		return err
	}
	raw := append([]byte(nil), s.data[start:s.off]...)
	return v.Addr().Interface().(Unmarshaler).UnmarshalPHP(raw)
}

func decodeBool(s *decodeState, v reflect.Value) error {
	off := s.off
	tag, err := s.peek()
	if err != nil {
		return err
	}
	switch {
	case tag == 'b':
		b, err := s.parseBool()
		if err != nil {
			return err
		}
		v.SetBool(b)
		return nil
	case s.cfg.weakTypes && tag == 'i':
		n, err := s.parseInt()
		if err != nil {
			return err
		}
		v.SetBool(n != 0)
		return nil
	case s.cfg.weakTypes && tag == 'd':
		f, err := s.parseFloat()
		if err != nil {
			return err
		}
		v.SetBool(f != 0)
		return nil
	case s.cfg.weakTypes && (tag == 's' || tag == 'S'):
		b, err := s.parseStringBytes()
		if err != nil {
			return err
		}
		// PHP の真偽値キャスト: "" と "0" だけが false
		v.SetBool(len(b) != 0 && string(b) != "0")
		return nil
	}
	return s.typeErr(off, tag, v.Type())
}

func decodeInt(s *decodeState, v reflect.Value) error {
	off := s.off
	tag, err := s.peek()
	if err != nil {
		return err
	}
	var n int64
	switch {
	case tag == 'i':
		if n, err = s.parseInt(); err != nil {
			return err
		}
	case s.cfg.weakTypes && (tag == 's' || tag == 'S'):
		b, err := s.parseStringBytes()
		if err != nil {
			return err
		}
		if n, err = strconv.ParseInt(string(b), 10, 64); err != nil {
			return s.typeErr(off, tag, v.Type())
		}
	case s.cfg.weakTypes && tag == 'd':
		f, err := s.parseFloat()
		if err != nil {
			return err
		}
		if math.IsNaN(f) || f < math.MinInt64 || f >= math.MaxInt64 {
			return s.typeErr(off, tag, v.Type())
		}
		n = int64(f) // PHP の int キャストと同じくゼロ方向へ切り捨て
	case s.cfg.weakTypes && tag == 'b':
		b, err := s.parseBool()
		if err != nil {
			return err
		}
		if b {
			n = 1
		}
	default:
		return s.typeErr(off, tag, v.Type())
	}
	if v.OverflowInt(n) {
		return s.typeErr(off, tag, v.Type())
	}
	v.SetInt(n)
	return nil
}

func decodeUint(s *decodeState, v reflect.Value) error {
	off := s.off
	tag, err := s.peek()
	if err != nil {
		return err
	}
	var n int64
	switch {
	case tag == 'i':
		if n, err = s.parseInt(); err != nil {
			return err
		}
	case s.cfg.weakTypes && (tag == 's' || tag == 'S'):
		b, err := s.parseStringBytes()
		if err != nil {
			return err
		}
		if n, err = strconv.ParseInt(string(b), 10, 64); err != nil {
			return s.typeErr(off, tag, v.Type())
		}
	case s.cfg.weakTypes && tag == 'b':
		b, err := s.parseBool()
		if err != nil {
			return err
		}
		if b {
			n = 1
		}
	default:
		return s.typeErr(off, tag, v.Type())
	}
	if n < 0 || v.OverflowUint(uint64(n)) {
		return s.typeErr(off, tag, v.Type())
	}
	v.SetUint(uint64(n))
	return nil
}

func decodeFloat(s *decodeState, v reflect.Value) error {
	off := s.off
	tag, err := s.peek()
	if err != nil {
		return err
	}
	var f float64
	switch {
	case tag == 'd':
		if f, err = s.parseFloat(); err != nil {
			return err
		}
	case tag == 'i':
		// 整数 → 浮動小数点は情報を落とさないため常に許可
		n, err := s.parseInt()
		if err != nil {
			return err
		}
		f = float64(n)
	case s.cfg.weakTypes && (tag == 's' || tag == 'S'):
		b, err := s.parseStringBytes()
		if err != nil {
			return err
		}
		if f, err = strconv.ParseFloat(string(b), 64); err != nil {
			return s.typeErr(off, tag, v.Type())
		}
	case s.cfg.weakTypes && tag == 'b':
		b, err := s.parseBool()
		if err != nil {
			return err
		}
		if b {
			f = 1
		}
	default:
		return s.typeErr(off, tag, v.Type())
	}
	if v.OverflowFloat(f) {
		return s.typeErr(off, tag, v.Type())
	}
	v.SetFloat(f)
	return nil
}

func decodeString(s *decodeState, v reflect.Value) error {
	off := s.off
	tag, err := s.peek()
	if err != nil {
		return err
	}
	switch {
	case tag == 's' || tag == 'S':
		b, err := s.parseStringBytes()
		if err != nil {
			return err
		}
		v.SetString(string(b))
		return nil
	case s.cfg.weakTypes && tag == 'i':
		n, err := s.parseInt()
		if err != nil {
			return err
		}
		v.SetString(strconv.FormatInt(n, 10))
		return nil
	case s.cfg.weakTypes && tag == 'd':
		f, err := s.parseFloat()
		if err != nil {
			return err
		}
		v.SetString(string(appendFloatBody(nil, f)))
		return nil
	case s.cfg.weakTypes && tag == 'b':
		b, err := s.parseBool()
		if err != nil {
			return err
		}
		// PHP の文字列キャスト: true → "1" / false → ""
		if b {
			v.SetString("1")
		} else {
			v.SetString("")
		}
		return nil
	}
	return s.typeErr(off, tag, v.Type())
}

func decodeByteSlice(s *decodeState, v reflect.Value) error {
	off := s.off
	tag, err := s.peek()
	if err != nil {
		return err
	}
	if tag != 's' && tag != 'S' {
		return s.typeErr(off, tag, v.Type())
	}
	b, err := s.parseStringBytes()
	if err != nil {
		return err
	}
	v.SetBytes(append([]byte(nil), b...))
	return nil
}

func compileSliceDec(t reflect.Type) (decFunc, error) {
	return compileSliceDecMode(t, true)
}

func compileSliceDecMode(t reflect.Type, sparseEnabled bool) (decFunc, error) {
	elemPlan, err := decPlanFor(t.Elem())
	if err != nil {
		return nil, err
	}
	elemType := t.Elem()
	return withNull(func(s *decodeState, v reflect.Value) error {
		off := s.off
		tag, err := s.peek()
		if err != nil {
			return err
		}
		if tag != 'a' {
			return s.typeErr(off, tag, t)
		}
		if err := s.enter(); err != nil {
			return err
		}
		defer s.leave()
		n, err := s.parseArrayHeader()
		if err != nil {
			return err
		}
		// キーの検証を終えるまで一時領域にパース順で保持する
		idxs := make([]int64, 0, min(n, 1024))
		vals := reflect.MakeSlice(t, 0, min(n, 1024))
		maxKey := int64(-1)
		sparse := sparseEnabled && s.cfg.sparseArrayPadding
		for i := 0; i < n; i++ {
			keyOff := s.off
			ktag, err := s.peek()
			if err != nil {
				return err
			}
			var k int64
			switch ktag {
			case 'i':
				if k, err = s.parseInt(); err != nil {
					return err
				}
			case 's', 'S':
				// PHP は数値文字列キーを int に正規化する (unserialize 時も同様)
				b, err := s.parseStringBytes()
				if err != nil {
					return err
				}
				var ok bool
				if k, ok = canonicalIntString(string(b)); !ok {
					return &TypeError{Offset: keyOff, PHPType: "array with string key", GoType: t}
				}
			default:
				return &TypeError{Offset: keyOff, PHPType: "array with " + phpTypeName(ktag) + " key", GoType: t}
			}
			if k < 0 || (!sparse && k >= int64(n)) || (sparse && k > maxSparseArrayKey) {
				reason := "non-sequential"
				if sparse && k > maxSparseArrayKey {
					reason = "too large"
				}
				return &TypeError{Offset: keyOff, PHPType: fmt.Sprintf("array with %s key %d", reason, k), GoType: t}
			}
			if k > maxKey {
				maxKey = k
			}
			ev := reflect.New(elemType).Elem()
			if err := elemPlan(s, ev); err != nil {
				return err
			}
			idxs = append(idxs, k)
			vals = reflect.Append(vals, ev)
		}
		if err := s.expect('}'); err != nil {
			return err
		}
		outLen := n
		if sparse {
			outLen = int(maxKey + 1)
		}
		out := reflect.MakeSlice(t, outLen, outLen)
		var seen []bool
		if !sparse {
			seen = make([]bool, outLen)
		}
		for j, k := range idxs {
			if !sparse {
				if seen[k] {
					return &TypeError{Offset: off, PHPType: fmt.Sprintf("array with duplicate key %d", k), GoType: t}
				}
				seen[k] = true
			}
			out.Index(int(k)).Set(vals.Index(j))
		}
		v.Set(out)
		return nil
	}), nil
}

func compileArrayDec(t reflect.Type) (decFunc, error) {
	slicePlan, err := compileSliceDecMode(reflect.SliceOf(t.Elem()), false)
	if err != nil {
		return nil, err
	}
	sliceType := reflect.SliceOf(t.Elem())
	return withNull(func(s *decodeState, v reflect.Value) error {
		off := s.off
		sv := reflect.New(sliceType).Elem()
		if err := slicePlan(s, sv); err != nil {
			return err
		}
		if sv.Len() != t.Len() {
			return &TypeError{Offset: off, PHPType: fmt.Sprintf("array with %d elements", sv.Len()), GoType: t}
		}
		reflect.Copy(v, sv)
		return nil
	}), nil
}

func compileMapDec(t reflect.Type) (decFunc, error) {
	keyKind := t.Key().Kind()
	switch keyKind {
	case reflect.String,
		reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
	default:
		return nil, fmt.Errorf("phpserialize: unsupported map key type %s", t.Key())
	}
	elemPlan, err := decPlanFor(t.Elem())
	if err != nil {
		return nil, err
	}
	keyType := t.Key()
	elemType := t.Elem()
	return withNull(func(s *decodeState, v reflect.Value) error {
		off := s.off
		tag, err := s.peek()
		if err != nil {
			return err
		}
		if tag != 'a' && tag != 'O' {
			return s.typeErr(off, tag, t)
		}
		if err := s.enter(); err != nil {
			return err
		}
		defer s.leave()
		var n int
		if tag == 'a' {
			if n, err = s.parseArrayHeader(); err != nil {
				return err
			}
		} else {
			if _, n, err = s.parseObjectHeader(); err != nil {
				return err
			}
		}
		if v.IsNil() {
			v.Set(reflect.MakeMapWithSize(t, min(n, 1024)))
		}
		for i := 0; i < n; i++ {
			kv, err := decodeMapKey(s, keyType, tag == 'O')
			if err != nil {
				return err
			}
			ev := reflect.New(elemType).Elem()
			if err := elemPlan(s, ev); err != nil {
				return err
			}
			v.SetMapIndex(kv, ev) // 重複キーは後勝ち (PHP と同じ)
		}
		return s.expect('}')
	}), nil
}

// decodeMapKey は配列 / オブジェクトのキー 1 個を keyType の値として読み取る。
// PHP は数値文字列キーを int に正規化する (unserialize 時も同様) ため、
// int 系のキー型は正準形の数値文字列キーも受け付ける。isObject のときだけ
// プロパティ名のマングリングを正規化する。
func decodeMapKey(s *decodeState, keyType reflect.Type, isObject bool) (reflect.Value, error) {
	off := s.off
	ktag, err := s.peek()
	if err != nil {
		return reflect.Value{}, err
	}
	kv := reflect.New(keyType).Elem()

	// キーを int64 (数値キー) または string として読む
	var (
		isInt  bool
		intKey int64
		strKey string
	)
	switch ktag {
	case 'i':
		if intKey, err = s.parseInt(); err != nil {
			return reflect.Value{}, err
		}
		isInt = true
	case 's', 'S':
		b, err := s.parseStringBytes()
		if err != nil {
			return reflect.Value{}, err
		}
		if isObject {
			b = normalizePropName(b)
		}
		strKey = string(b)
		if !isObject {
			if n, ok := canonicalIntString(strKey); ok {
				intKey = n
				isInt = true
			}
		}
	default:
		return reflect.Value{}, &TypeError{Offset: off, PHPType: phpTypeName(ktag) + " key", GoType: keyType}
	}

	switch keyType.Kind() {
	case reflect.String:
		if isInt {
			kv.SetString(strconv.FormatInt(intKey, 10))
		} else {
			kv.SetString(strKey)
		}
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		if !isInt {
			if !s.cfg.weakTypes {
				return reflect.Value{}, &TypeError{Offset: off, PHPType: "string key", GoType: keyType}
			}
			n, err := strconv.ParseInt(strKey, 10, 64)
			if err != nil {
				return reflect.Value{}, &TypeError{Offset: off, PHPType: "string key", GoType: keyType}
			}
			intKey = n
		}
		if kv.OverflowInt(intKey) {
			return reflect.Value{}, &TypeError{Offset: off, PHPType: "int key", GoType: keyType}
		}
		kv.SetInt(intKey)
	default: // uint 系
		if !isInt {
			if !s.cfg.weakTypes {
				return reflect.Value{}, &TypeError{Offset: off, PHPType: "string key", GoType: keyType}
			}
			n, err := strconv.ParseInt(strKey, 10, 64)
			if err != nil {
				return reflect.Value{}, &TypeError{Offset: off, PHPType: "string key", GoType: keyType}
			}
			intKey = n
		}
		if intKey < 0 || kv.OverflowUint(uint64(intKey)) {
			return reflect.Value{}, &TypeError{Offset: off, PHPType: "int key", GoType: keyType}
		}
		kv.SetUint(uint64(intKey))
	}
	return kv, nil
}

func compilePointerDec(t reflect.Type) (decFunc, error) {
	elemPlan, err := decPlanFor(t.Elem())
	if err != nil {
		return nil, err
	}
	elemType := t.Elem()
	return withNull(func(s *decodeState, v reflect.Value) error {
		if v.IsNil() {
			v.Set(reflect.New(elemType))
		}
		return elemPlan(s, v.Elem())
	}), nil
}

func compileStructDec(t reflect.Type) (decFunc, error) {
	fields := structFields(t)
	byName := make(map[string]*structField, len(fields))
	byLower := make(map[string]*structField, len(fields))
	plans := make([]decFunc, len(fields))
	for i := range fields {
		f := &fields[i]
		p, err := decPlanFor(f.typ)
		if err != nil {
			return nil, err
		}
		plans[i] = p
		f.planIdx = i
		byName[f.name] = f
		lower := strings.ToLower(f.name)
		if _, ok := byLower[lower]; !ok {
			byLower[lower] = f
		}
	}
	return withNull(func(s *decodeState, v reflect.Value) error {
		off := s.off
		tag, err := s.peek()
		if err != nil {
			return err
		}
		if tag != 'a' && tag != 'O' {
			return s.typeErr(off, tag, t)
		}
		if err := s.enter(); err != nil {
			return err
		}
		defer s.leave()
		var n int
		if tag == 'a' {
			if n, err = s.parseArrayHeader(); err != nil {
				return err
			}
		} else {
			if _, n, err = s.parseObjectHeader(); err != nil {
				return err
			}
		}
		for i := 0; i < n; i++ {
			ktag, err := s.peek()
			if err != nil {
				return err
			}
			var name []byte
			switch ktag {
			case 's', 'S':
				b, err := s.parseStringBytes()
				if err != nil {
					return err
				}
				name = normalizePropName(b)
			case 'i':
				k, err := s.parseInt()
				if err != nil {
					return err
				}
				name = strconv.AppendInt(nil, k, 10)
			default:
				return &TypeError{Offset: s.off, PHPType: phpTypeName(ktag) + " key", GoType: t}
			}
			f := byName[string(name)]
			if f == nil {
				f = byLower[strings.ToLower(string(name))]
			}
			if f == nil {
				if err := s.skipValue(); err != nil {
					return err
				}
				continue
			}
			fv, err := fieldByIndexAlloc(v, f.index)
			if err != nil {
				return err
			}
			if err := plans[f.planIdx](s, fv); err != nil {
				return err
			}
		}
		return s.expect('}')
	}), nil
}

// fieldByIndexAlloc は index パスを辿ってフィールドを返す。
// 途中の埋め込みポインタが nil なら割り当てる。
func fieldByIndexAlloc(v reflect.Value, index []int) (reflect.Value, error) {
	for i, x := range index {
		if i > 0 {
			if v.Kind() == reflect.Pointer {
				if v.IsNil() {
					if !v.CanSet() {
						return reflect.Value{}, fmt.Errorf("phpserialize: cannot allocate embedded pointer %s", v.Type())
					}
					v.Set(reflect.New(v.Type().Elem()))
				}
				v = v.Elem()
			}
		}
		v = v.Field(x)
	}
	return v, nil
}

// decodeAnyValue は any (空インターフェース) へのデコード。
func decodeAnyValue(s *decodeState, v reflect.Value) error {
	val, err := decodeAny(s)
	if err != nil {
		return err
	}
	if val == nil {
		v.SetZero()
		return nil
	}
	v.Set(reflect.ValueOf(val))
	return nil
}

// decodeAny は型指定なしで値を Go の素の型へデコードする。
// list 形状 (キーが 0..n-1 で昇順) → []any、それ以外の配列 / オブジェクト → map[string]any。
func decodeAny(s *decodeState) (any, error) {
	tag, err := s.peek()
	if err != nil {
		return nil, err
	}
	switch tag {
	case 'N':
		return nil, s.parseNull()
	case 'b':
		return s.parseBool()
	case 'i':
		return s.parseInt()
	case 'd':
		return s.parseFloat()
	case 's', 'S':
		b, err := s.parseStringBytes()
		if err != nil {
			return nil, err
		}
		return string(b), nil
	case 'a':
		if err := s.enter(); err != nil {
			return nil, err
		}
		defer s.leave()
		n, err := s.parseArrayHeader()
		if err != nil {
			return nil, err
		}
		// PHP と同じく数値文字列キーは int に正規化し、重複キーは後勝ちで統合する。
		intKeys := make(map[int64]any, min(n, 1024))
		var strKeys map[string]any
		for i := 0; i < n; i++ {
			ktag, err := s.peek()
			if err != nil {
				return nil, err
			}
			var (
				ik    int64
				sk    string
				isInt bool
			)
			switch ktag {
			case 'i':
				if ik, err = s.parseInt(); err != nil {
					return nil, err
				}
				isInt = true
			case 's', 'S':
				b, err := s.parseStringBytes()
				if err != nil {
					return nil, err
				}
				sk = string(b)
				if v, ok := canonicalIntString(sk); ok {
					ik = v
					isInt = true
				}
			default:
				return nil, &SyntaxError{Offset: s.off, Msg: "array key must be int or string, got " + phpTypeName(ktag)}
			}
			val, err := decodeAny(s)
			if err != nil {
				return nil, err
			}
			if isInt {
				intKeys[ik] = val
			} else {
				if strKeys == nil {
					strKeys = make(map[string]any)
				}
				strKeys[sk] = val
			}
		}
		if err := s.expect('}'); err != nil {
			return nil, err
		}
		// list 形状 (int キーの集合がちょうど {0..n-1}) → []any
		if len(strKeys) == 0 {
			isList := true
			for k := range intKeys {
				if k < 0 || k >= int64(len(intKeys)) {
					isList = false
					break
				}
			}
			if isList {
				out := make([]any, len(intKeys))
				for k, v := range intKeys {
					out[k] = v
				}
				return out, nil
			}
		}
		out := make(map[string]any, len(intKeys)+len(strKeys))
		for k, v := range intKeys {
			out[strconv.FormatInt(k, 10)] = v
		}
		for k, v := range strKeys {
			out[k] = v
		}
		return out, nil
	case 'O':
		if err := s.enter(); err != nil {
			return nil, err
		}
		defer s.leave()
		_, n, err := s.parseObjectHeader()
		if err != nil {
			return nil, err
		}
		out := make(map[string]any, min(n, 1024))
		for i := 0; i < n; i++ {
			ktag, err := s.peek()
			if err != nil {
				return nil, err
			}
			var key string
			switch ktag {
			case 's', 'S':
				b, err := s.parseStringBytes()
				if err != nil {
					return nil, err
				}
				key = string(normalizePropName(b))
			case 'i':
				kn, err := s.parseInt()
				if err != nil {
					return nil, err
				}
				key = strconv.FormatInt(kn, 10)
			default:
				return nil, &SyntaxError{Offset: s.off, Msg: "property key must be int or string, got " + phpTypeName(ktag)}
			}
			val, err := decodeAny(s)
			if err != nil {
				return nil, err
			}
			out[key] = val
		}
		return out, s.expect('}')
	case 'r', 'R', 'C', 'E':
		return nil, fmt.Errorf("%w: %s at offset %d", ErrUnsupported, phpTypeName(tag), s.off)
	}
	return nil, s.syntaxErr(s.off, "unexpected token %q", tag)
}
