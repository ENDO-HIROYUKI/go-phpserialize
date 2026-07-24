package phpserialize

import (
	"reflect"
	"testing"
)

var fuzzSeeds = []string{
	`N;`,
	`b:1;`,
	`i:123;`,
	`i:-9223372036854775808;`,
	`d:0.1;`,
	`d:INF;`,
	`d:NAN;`,
	`s:12:"プロレス";`,
	`S:3:"\61bc";`,
	`a:0:{}`,
	`a:2:{i:0;s:1:"a";i:1;s:1:"b";}`,
	`a:2:{i:0;s:1:"a";i:5;s:1:"b";}`, // trim21 v0.1.2 が panic した入力
	`a:1:{i:1;s:1:"a";}`,
	`a:2:{s:1:"k";i:1;i:2;s:1:"v";}`,
	`a:1:{s:5:"sizes";a:1:{s:9:"thumbnail";a:2:{s:4:"file";s:5:"t.jpg";s:5:"width";i:150;}}}`,
	"O:1:\"P\":3:{s:3:\"pub\";i:1;s:6:\"\x00*\x00pro\";i:2;s:6:\"\x00P\x00pri\";i:3;}",
	`a:2:{s:2:"r1";a:1:{s:1:"x";i:1;}s:2:"r2";R:2;}`,
	`E:11:"Suit:Hearts";`,
	`C:9:"ClassName":15:{a:1:{i:0;i:1;}}`,
	`a:99999999:{i:0;s:1:"a";}`,
	`s:9223372036854775807:"a";`,
	`O:9223372036854775807:"P":1:{s:1:"a";i:1;}`,
	`i:99999999999999999999;`,
	`s:11:"プロレス";`,
	`d:;`,
	`b:2;`,
	``,
}

// FuzzUnmarshalNoPanic: どんな入力でも panic しないことを保証する (本ライブラリの最重要仕様)。
func FuzzUnmarshalNoPanic(f *testing.F) {
	for _, s := range fuzzSeeds {
		f.Add([]byte(s))
	}
	type sample struct {
		A []string         `php:"a"`
		B map[string]int64 `php:"b"`
		C *attachmentMeta  `php:"c"`
		D any              `php:"d"`
	}
	dec := NewDecoder(WithWeakTypes(), WithAllowTrailingData())
	f.Fuzz(func(t *testing.T, data []byte) {
		var v1 any
		_ = Unmarshal(data, &v1)
		var v2 []string
		_ = Unmarshal(data, &v2)
		var v3 map[string]any
		_ = Unmarshal(data, &v3)
		var v4 sample
		_ = Unmarshal(data, &v4)
		var v5 sample
		_ = dec.Unmarshal(data, &v5)
	})
}

// 疎配列では入力キーから復元長を決めるため、過大キーを含む任意入力でも panic しないことを検証する。
func FuzzSparsePaddingNoPanic(f *testing.F) {
	for _, s := range []string{
		`a:2:{i:0;s:1:"a";i:5;s:1:"b";}`,
		`a:2:{i:0;s:1:"a";i:0;s:1:"b";}`,
		`a:1:{i:1048576;i:1;}`,
		`a:1:{i:-1;s:1:"a";}`,
		`a:1:{s:1:"5";s:1:"a";}`,
	} {
		f.Add([]byte(s))
	}
	type sample struct {
		Items []string `php:"items"`
	}
	dec := NewDecoder(WithSparseArrayPadding())
	f.Fuzz(func(t *testing.T, data []byte) {
		var strings []string
		_ = dec.Unmarshal(data, &strings)
		var nested [][]string
		_ = dec.Unmarshal(data, &nested)
		var inStruct sample
		_ = dec.Unmarshal(data, &inStruct)
		var array [4]string
		_ = dec.Unmarshal(data, &array)
	})
}

// FuzzRoundTrip: デコードに成功した値は Marshal → Unmarshal で同値に戻る。
func FuzzRoundTrip(f *testing.F) {
	for _, s := range fuzzSeeds {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		var v any
		if err := Unmarshal(data, &v); err != nil {
			return // 不正入力は対象外
		}
		b, err := Marshal(v)
		if err != nil {
			t.Fatalf("デコード済みの値の Marshal が失敗: %v (input %q)", err, data)
		}
		var v2 any
		if err := Unmarshal(b, &v2); err != nil {
			t.Fatalf("Marshal 出力の Unmarshal が失敗: %v (marshaled %q)", err, b)
		}
		if !anyEqual(v, v2) {
			t.Fatalf("round trip mismatch:\n in  %#v\n out %#v\n bytes %q", v, v2, b)
		}
	})
}

// anyEqual は NaN を等値として扱う DeepEqual。
func anyEqual(a, b any) bool {
	if af, ok := a.(float64); ok {
		if bf, ok := b.(float64); ok {
			return (af != af && bf != bf) || af == bf
		}
		return false
	}
	switch av := a.(type) {
	case []any:
		bv, ok := b.([]any)
		if !ok || len(av) != len(bv) {
			return false
		}
		for i := range av {
			if !anyEqual(av[i], bv[i]) {
				return false
			}
		}
		return true
	case map[string]any:
		bv, ok := b.(map[string]any)
		if !ok || len(av) != len(bv) {
			return false
		}
		for k, x := range av {
			y, ok := bv[k]
			if !ok || !anyEqual(x, y) {
				return false
			}
		}
		return true
	}
	return reflect.DeepEqual(a, b)
}
