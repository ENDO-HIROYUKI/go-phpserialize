package phpserialize

import (
	"fmt"
	"math"
	"reflect"
	"sort"
	"strconv"
	"sync"
)

// encBufPool はエンコードバッファの再利用プール。状態汚染を持ち込まないよう
// []byte だけをプールし、encodeState (depth / cfg) は呼び出しごとに作る。
// 初期容量 512 は WP メタ等の典型ペイロードを 1 確保で収める目安。
var encBufPool = sync.Pool{New: func() any { b := make([]byte, 0, 512); return &b }}

// maxPooledEncBuf を超える容量のバッファはプールへ戻さない
// (巨大 Marshal 1 回によるメモリ滞留と、エンコード済みデータの長期保持を防ぐ)。
const maxPooledEncBuf = 256 << 10

// Marshal は v を PHP シリアライズ形式にエンコードする。
//
// 決定性のため map はキーをソートして出力する (int 昇順 → string バイト順)。
// PHP は挿入順を保持するため、この点は意図的な相違 (ドキュメント参照)。
func (e *Encoder) Marshal(v any) ([]byte, error) {
	if v == nil {
		return []byte("N;"), nil
	}
	rv := reflect.ValueOf(v)
	plan, err := encPlanFor(rv.Type())
	if err != nil {
		return nil, err
	}
	bp := encBufPool.Get().(*[]byte)
	s := &encodeState{buf: (*bp)[:0], cfg: &e.cfg}
	// 返り値のコピーを作った後にのみバッファをプールへ戻す (成否・panic を問わず返却)。
	defer func() {
		if cap(s.buf) <= maxPooledEncBuf {
			*bp = s.buf[:0]
			encBufPool.Put(bp)
		}
	}()
	if err := plan(s, rv); err != nil {
		return nil, err
	}
	out := make([]byte, len(s.buf))
	copy(out, s.buf)
	return out, nil
}

type encodeState struct {
	buf   []byte
	depth int
	cfg   *config
}

func (s *encodeState) enter() error {
	s.depth++
	if s.depth > s.cfg.maxDepth {
		// 循環参照もここで検出される (深さ上限で必ず止まる)
		return ErrDepth
	}
	return nil
}

func (s *encodeState) leave() { s.depth-- }

type encFunc func(s *encodeState, v reflect.Value) error

var encPlans sync.Map // reflect.Type -> encFunc

var marshalerType = reflect.TypeFor[Marshaler]()

func encPlanFor(t reflect.Type) (encFunc, error) {
	if f, ok := encPlans.Load(t); ok {
		return f.(encFunc), nil
	}
	var (
		wg sync.WaitGroup
		f  encFunc
	)
	wg.Add(1)
	indirect := encFunc(func(s *encodeState, v reflect.Value) error {
		wg.Wait()
		return f(s, v)
	})
	if actual, loaded := encPlans.LoadOrStore(t, indirect); loaded {
		return actual.(encFunc), nil
	}
	compiled, err := compileEnc(t)
	if err != nil {
		compiled = func(*encodeState, reflect.Value) error { return err }
	}
	f = compiled
	wg.Done()
	encPlans.Store(t, compiled)
	return compiled, err
}

func compileEnc(t reflect.Type) (encFunc, error) {
	if t.Implements(marshalerType) {
		return encodeViaMarshaler, nil
	}
	if t.Kind() != reflect.Pointer && reflect.PointerTo(t).Implements(marshalerType) {
		return encodeViaAddrMarshaler, nil
	}
	switch t.Kind() {
	case reflect.Bool:
		return encodeBool, nil
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return encodeInt, nil
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return encodeUint, nil
	case reflect.Float32, reflect.Float64:
		return encodeFloat, nil
	case reflect.String:
		return encodeString, nil
	case reflect.Slice:
		if t.Elem().Kind() == reflect.Uint8 {
			return encodeByteSlice, nil
		}
		return compileSliceEnc(t)
	case reflect.Array:
		return compileSliceEnc(t)
	case reflect.Map:
		return compileMapEnc(t)
	case reflect.Struct:
		return compileStructEnc(t)
	case reflect.Pointer:
		return compilePointerEnc(t)
	case reflect.Interface:
		return encodeInterface, nil
	}
	return nil, fmt.Errorf("phpserialize: unsupported encode type %s", t)
}

func encodeViaMarshaler(s *encodeState, v reflect.Value) error {
	if v.Kind() == reflect.Pointer && v.IsNil() {
		s.buf = append(s.buf, "N;"...)
		return nil
	}
	b, err := v.Interface().(Marshaler).MarshalPHP()
	if err != nil {
		return err
	}
	s.buf = append(s.buf, b...)
	return nil
}

func encodeViaAddrMarshaler(s *encodeState, v reflect.Value) error {
	if v.CanAddr() {
		return encodeViaMarshaler(s, v.Addr())
	}
	// アドレスを取れない値はコピーしてから呼ぶ
	p := reflect.New(v.Type())
	p.Elem().Set(v)
	return encodeViaMarshaler(s, p)
}

func encodeBool(s *encodeState, v reflect.Value) error {
	if v.Bool() {
		s.buf = append(s.buf, "b:1;"...)
	} else {
		s.buf = append(s.buf, "b:0;"...)
	}
	return nil
}

func encodeInt(s *encodeState, v reflect.Value) error {
	s.buf = append(s.buf, 'i', ':')
	s.buf = strconv.AppendInt(s.buf, v.Int(), 10)
	s.buf = append(s.buf, ';')
	return nil
}

func encodeUint(s *encodeState, v reflect.Value) error {
	n := v.Uint()
	if n > math.MaxInt64 {
		// PHP の int は 64bit 符号付き。表現できない値は暗黙変換せずエラーにする
		return fmt.Errorf("phpserialize: uint value %d overflows PHP int", n)
	}
	s.buf = append(s.buf, 'i', ':')
	s.buf = strconv.AppendUint(s.buf, n, 10)
	s.buf = append(s.buf, ';')
	return nil
}

// appendFloatBody は PHP の serialize_precision=-1 (最短で往復可能な表現) 相当で
// float を文字列化する。INF / -INF / NAN は PHP のトークンをそのまま使う。
func appendFloatBody(b []byte, f float64) []byte {
	switch {
	case math.IsNaN(f):
		return append(b, "NAN"...)
	case math.IsInf(f, 1):
		return append(b, "INF"...)
	case math.IsInf(f, -1):
		return append(b, "-INF"...)
	}
	return strconv.AppendFloat(b, f, 'G', -1, 64)
}

func encodeFloat(s *encodeState, v reflect.Value) error {
	s.buf = append(s.buf, 'd', ':')
	s.buf = appendFloatBody(s.buf, v.Float())
	s.buf = append(s.buf, ';')
	return nil
}

func appendString(b []byte, str string) []byte {
	b = append(b, 's', ':')
	b = strconv.AppendInt(b, int64(len(str)), 10)
	b = append(b, ':', '"')
	b = append(b, str...)
	return append(b, '"', ';')
}

func encodeString(s *encodeState, v reflect.Value) error {
	s.buf = appendString(s.buf, v.String())
	return nil
}

func encodeByteSlice(s *encodeState, v reflect.Value) error {
	if v.IsNil() {
		s.buf = append(s.buf, "N;"...)
		return nil
	}
	s.buf = append(s.buf, 's', ':')
	s.buf = strconv.AppendInt(s.buf, int64(v.Len()), 10)
	s.buf = append(s.buf, ':', '"')
	s.buf = append(s.buf, v.Bytes()...)
	s.buf = append(s.buf, '"', ';')
	return nil
}

func compileSliceEnc(t reflect.Type) (encFunc, error) {
	elemPlan, err := encPlanFor(t.Elem())
	if err != nil {
		return nil, err
	}
	isSlice := t.Kind() == reflect.Slice
	return func(s *encodeState, v reflect.Value) error {
		if isSlice && v.IsNil() {
			s.buf = append(s.buf, "N;"...)
			return nil
		}
		if err := s.enter(); err != nil {
			return err
		}
		defer s.leave()
		n := v.Len()
		s.buf = append(s.buf, 'a', ':')
		s.buf = strconv.AppendInt(s.buf, int64(n), 10)
		s.buf = append(s.buf, ':', '{')
		for i := 0; i < n; i++ {
			s.buf = append(s.buf, 'i', ':')
			s.buf = strconv.AppendInt(s.buf, int64(i), 10)
			s.buf = append(s.buf, ';')
			if err := elemPlan(s, v.Index(i)); err != nil {
				return err
			}
		}
		s.buf = append(s.buf, '}')
		return nil
	}, nil
}

func compileMapEnc(t reflect.Type) (encFunc, error) {
	keyKind := t.Key().Kind()
	var keyIsInt, keyIsUint, keyIsString bool
	switch keyKind {
	case reflect.String:
		keyIsString = true
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		keyIsInt = true
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		keyIsUint = true
	default:
		return nil, fmt.Errorf("phpserialize: unsupported map key type %s", t.Key())
	}
	elemPlan, err := encPlanFor(t.Elem())
	if err != nil {
		return nil, err
	}
	return func(s *encodeState, v reflect.Value) error {
		if v.IsNil() {
			s.buf = append(s.buf, "N;"...)
			return nil
		}
		if err := s.enter(); err != nil {
			return err
		}
		defer s.leave()
		keys := v.MapKeys()
		switch {
		case keyIsInt:
			sort.Slice(keys, func(i, j int) bool { return keys[i].Int() < keys[j].Int() })
		case keyIsUint:
			sort.Slice(keys, func(i, j int) bool { return keys[i].Uint() < keys[j].Uint() })
		case keyIsString:
			sort.Slice(keys, func(i, j int) bool { return keys[i].String() < keys[j].String() })
		}
		s.buf = append(s.buf, 'a', ':')
		s.buf = strconv.AppendInt(s.buf, int64(len(keys)), 10)
		s.buf = append(s.buf, ':', '{')
		for _, k := range keys {
			switch {
			case keyIsInt:
				s.buf = append(s.buf, 'i', ':')
				s.buf = strconv.AppendInt(s.buf, k.Int(), 10)
				s.buf = append(s.buf, ';')
			case keyIsUint:
				n := k.Uint()
				if n > math.MaxInt64 {
					return fmt.Errorf("phpserialize: map key %d overflows PHP int", n)
				}
				s.buf = append(s.buf, 'i', ':')
				s.buf = strconv.AppendUint(s.buf, n, 10)
				s.buf = append(s.buf, ';')
			default:
				s.buf = appendMapStringKey(s.buf, k.String())
			}
			if err := elemPlan(s, v.MapIndex(k)); err != nil {
				return err
			}
		}
		s.buf = append(s.buf, '}')
		return nil
	}, nil
}

// appendMapStringKey は string キーを出力する。PHP は数値文字列のキーを int に
// 正規化するため、同じ規則 (先頭ゼロ・"+" なしの正準 10 進表記のみ) で i: に変換する。
func appendMapStringKey(b []byte, key string) []byte {
	if n, ok := canonicalIntString(key); ok {
		b = append(b, 'i', ':')
		b = strconv.AppendInt(b, n, 10)
		return append(b, ';')
	}
	return appendString(b, key)
}

// canonicalIntString は PHP が配列キーとして int に正規化する文字列かを判定する。
// ("5" → true / "05", "+5", "-0", "" → false)
func canonicalIntString(s string) (int64, bool) {
	if s == "" || s == "-" {
		return 0, false
	}
	body := s
	if s[0] == '-' {
		body = s[1:]
		if body == "" || body == "0" { // "-0" は正規化されない
			return 0, false
		}
	}
	if len(body) > 1 && body[0] == '0' { // 先頭ゼロは正規化されない
		return 0, false
	}
	for i := 0; i < len(body); i++ {
		if body[i] < '0' || body[i] > '9' {
			return 0, false
		}
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, false
	}
	return n, true
}

func compileStructEnc(t reflect.Type) (encFunc, error) {
	fields := structFields(t)
	plans := make([]encFunc, len(fields))
	for i := range fields {
		p, err := encPlanFor(fields[i].typ)
		if err != nil {
			return nil, err
		}
		plans[i] = p
	}
	return func(s *encodeState, v reflect.Value) error {
		if err := s.enter(); err != nil {
			return err
		}
		defer s.leave()
		// 1 パス目: 出力するフィールドを数える (omitempty / nil 埋め込み経路)
		type entry struct {
			fi int
			fv reflect.Value
		}
		entries := make([]entry, 0, len(fields))
		for i := range fields {
			fv, ok := fieldByIndexRead(v, fields[i].index)
			if !ok {
				continue
			}
			if fields[i].omitEmpty && isEmptyValue(fv) {
				continue
			}
			entries = append(entries, entry{fi: i, fv: fv})
		}
		s.buf = append(s.buf, 'a', ':')
		s.buf = strconv.AppendInt(s.buf, int64(len(entries)), 10)
		s.buf = append(s.buf, ':', '{')
		for _, e := range entries {
			s.buf = appendMapStringKey(s.buf, fields[e.fi].name)
			if err := plans[e.fi](s, e.fv); err != nil {
				return err
			}
		}
		s.buf = append(s.buf, '}')
		return nil
	}, nil
}

// fieldByIndexRead は読み取り用に index パスを辿る。途中の nil ポインタは到達不能として false。
func fieldByIndexRead(v reflect.Value, index []int) (reflect.Value, bool) {
	for i, x := range index {
		if i > 0 {
			if v.Kind() == reflect.Pointer {
				if v.IsNil() {
					return reflect.Value{}, false
				}
				v = v.Elem()
			}
		}
		v = v.Field(x)
	}
	return v, true
}

func isEmptyValue(v reflect.Value) bool {
	switch v.Kind() {
	case reflect.Array, reflect.Map, reflect.Slice, reflect.String:
		return v.Len() == 0
	case reflect.Bool,
		reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr,
		reflect.Float32, reflect.Float64:
		return v.IsZero()
	case reflect.Interface, reflect.Pointer:
		return v.IsNil()
	}
	return false
}

func compilePointerEnc(t reflect.Type) (encFunc, error) {
	elemPlan, err := encPlanFor(t.Elem())
	if err != nil {
		return nil, err
	}
	return func(s *encodeState, v reflect.Value) error {
		if v.IsNil() {
			s.buf = append(s.buf, "N;"...)
			return nil
		}
		return elemPlan(s, v.Elem())
	}, nil
}

func encodeInterface(s *encodeState, v reflect.Value) error {
	if v.IsNil() {
		s.buf = append(s.buf, "N;"...)
		return nil
	}
	elem := v.Elem()
	plan, err := encPlanFor(elem.Type())
	if err != nil {
		return err
	}
	return plan(s, elem)
}
