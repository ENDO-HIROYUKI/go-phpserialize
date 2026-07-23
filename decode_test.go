package phpserialize

import (
	"errors"
	"math"
	"reflect"
	"testing"
)

// 以下のシリアライズ済み文字列は PHP 8.4.21 の serialize() 実出力から採取したもの。

type attachmentSize struct {
	File     string `php:"file"`
	Width    int    `php:"width"`
	Height   int    `php:"height"`
	MimeType string `php:"mime_type"`
	Filesize *int   `php:"filesize"`
}

type attachmentMeta struct {
	Width    int                       `php:"width"`
	Height   int                       `php:"height"`
	File     string                    `php:"file"`
	Filesize *int                      `php:"filesize"`
	Sizes    map[string]attachmentSize `php:"sizes"`
}

func TestUnmarshalScalars(t *testing.T) {
	t.Run("null はゼロ値", func(t *testing.T) {
		s := "keep"
		if err := Unmarshal([]byte(`N;`), &s); err != nil {
			t.Fatal(err)
		}
		if s != "" {
			t.Errorf("got %q, want empty", s)
		}
	})

	t.Run("bool", func(t *testing.T) {
		var b bool
		if err := Unmarshal([]byte(`b:1;`), &b); err != nil || !b {
			t.Errorf("b:1; → %v, %v", b, err)
		}
		if err := Unmarshal([]byte(`b:0;`), &b); err != nil || b {
			t.Errorf("b:0; → %v, %v", b, err)
		}
		// PHP と同様 0/1 以外は構文エラー
		if err := Unmarshal([]byte(`b:2;`), &b); err == nil {
			t.Error("b:2; がエラーにならない")
		}
	})

	t.Run("int", func(t *testing.T) {
		var n int64
		for _, tc := range []struct {
			in   string
			want int64
		}{
			{`i:123;`, 123},
			{`i:-123;`, -123},
			{`i:0;`, 0},
			{`i:9223372036854775807;`, math.MaxInt64},
			{`i:-9223372036854775808;`, math.MinInt64},
		} {
			if err := Unmarshal([]byte(tc.in), &n); err != nil || n != tc.want {
				t.Errorf("%s → %d, %v (want %d)", tc.in, n, err, tc.want)
			}
		}
		// int64 範囲外は厳密エラー (PHP はクランプするが意図的な相違)
		if err := Unmarshal([]byte(`i:99999999999999999999;`), &n); err == nil {
			t.Error("オーバーフローがエラーにならない")
		}
		var n8 int8
		if err := Unmarshal([]byte(`i:200;`), &n8); err == nil {
			t.Error("int8 オーバーフローがエラーにならない")
		}
	})

	t.Run("float", func(t *testing.T) {
		var f float64
		for _, tc := range []struct {
			in   string
			want float64
		}{
			{`d:0.1;`, 0.1},
			{`d:0.3333333333333333;`, 1.0 / 3.0},
			{`d:1;`, 1}, // PHP は整数形式の double も受け付ける
			{`i:5;`, 5}, // int → float は常に許可
			{`d:1.0E+15;`, 1e15},
		} {
			if err := Unmarshal([]byte(tc.in), &f); err != nil || f != tc.want {
				t.Errorf("%s → %v, %v (want %v)", tc.in, f, err, tc.want)
			}
		}
		if err := Unmarshal([]byte(`d:INF;`), &f); err != nil || !math.IsInf(f, 1) {
			t.Errorf("d:INF; → %v, %v", f, err)
		}
		if err := Unmarshal([]byte(`d:-INF;`), &f); err != nil || !math.IsInf(f, -1) {
			t.Errorf("d:-INF; → %v, %v", f, err)
		}
		if err := Unmarshal([]byte(`d:NAN;`), &f); err != nil || !math.IsNaN(f) {
			t.Errorf("d:NAN; → %v, %v", f, err)
		}
	})

	t.Run("string はバイト長", func(t *testing.T) {
		var s string
		if err := Unmarshal([]byte(`s:12:"プロレス";`), &s); err != nil || s != "プロレス" {
			t.Errorf("got %q, %v", s, err)
		}
		// 長さ不一致はエラー
		if err := Unmarshal([]byte(`s:11:"プロレス";`), &s); err == nil {
			t.Error("バイト長不一致がエラーにならない")
		}
	})

	t.Run("S: 形式 (hex エスケープ)", func(t *testing.T) {
		var s string
		if err := Unmarshal([]byte(`S:3:"\61bc";`), &s); err != nil || s != "abc" {
			t.Errorf("got %q, %v", s, err)
		}
	})

	t.Run("[]byte", func(t *testing.T) {
		var b []byte
		if err := Unmarshal([]byte(`s:3:"abc";`), &b); err != nil || string(b) != "abc" {
			t.Errorf("got %q, %v", b, err)
		}
	})
}

func TestUnmarshalSlice(t *testing.T) {
	t.Run("list", func(t *testing.T) {
		var got []string
		if err := Unmarshal([]byte(`a:2:{i:0;s:1:"a";i:1;s:1:"b";}`), &got); err != nil {
			t.Fatal(err)
		}
		if want := []string{"a", "b"}; !reflect.DeepEqual(got, want) {
			t.Errorf("got %v", got)
		}
	})

	t.Run("キー順不同でも成功", func(t *testing.T) {
		var got []string
		if err := Unmarshal([]byte(`a:2:{i:1;s:1:"b";i:0;s:1:"a";}`), &got); err != nil {
			t.Fatal(err)
		}
		if want := []string{"a", "b"}; !reflect.DeepEqual(got, want) {
			t.Errorf("got %v", got)
		}
	})

	t.Run("空配列", func(t *testing.T) {
		var got []string
		if err := Unmarshal([]byte(`a:0:{}`), &got); err != nil {
			t.Fatal(err)
		}
		if len(got) != 0 {
			t.Errorf("got %v", got)
		}
	})

	t.Run("非連続キーはエラー (panic しない)", func(t *testing.T) {
		var got []string
		var te *TypeError
		if err := Unmarshal([]byte(`a:2:{i:0;s:1:"a";i:5;s:1:"b";}`), &got); !errors.As(err, &te) {
			t.Errorf("TypeError が返らない: %v", err)
		}
		if err := Unmarshal([]byte(`a:1:{i:1;s:1:"a";}`), &got); err == nil {
			t.Error("0 始まりでないキーがエラーにならない")
		}
		if err := Unmarshal([]byte(`a:1:{i:-1;s:1:"a";}`), &got); err == nil {
			t.Error("負のキーがエラーにならない")
		}
	})

	t.Run("重複キーはエラー", func(t *testing.T) {
		var got []string
		if err := Unmarshal([]byte(`a:2:{i:0;s:1:"a";i:0;s:1:"b";}`), &got); err == nil {
			t.Error("重複キーがエラーにならない")
		}
	})

	t.Run("文字列キーはエラー", func(t *testing.T) {
		var got []string
		if err := Unmarshal([]byte(`a:1:{s:3:"foo";s:1:"a";}`), &got); err == nil {
			t.Error("文字列キーがエラーにならない")
		}
	})

	t.Run("固定長配列", func(t *testing.T) {
		var got [2]string
		if err := Unmarshal([]byte(`a:2:{i:0;s:1:"a";i:1;s:1:"b";}`), &got); err != nil {
			t.Fatal(err)
		}
		if got != [2]string{"a", "b"} {
			t.Errorf("got %v", got)
		}
		if err := Unmarshal([]byte(`a:1:{i:0;s:1:"a";}`), &got); err == nil {
			t.Error("要素数不一致がエラーにならない")
		}
	})
}

func TestUnmarshalMap(t *testing.T) {
	t.Run("string キー map", func(t *testing.T) {
		got := map[string]int64{}
		if err := Unmarshal([]byte(`a:2:{s:1:"k";i:1;i:2;i:5;}`), &got); err != nil {
			t.Fatal(err)
		}
		// int キーは 10 進文字列化される
		if want := map[string]int64{"k": 1, "2": 5}; !reflect.DeepEqual(got, want) {
			t.Errorf("got %v", got)
		}
	})

	t.Run("int キー map と重複キー後勝ち", func(t *testing.T) {
		got := map[uint64]string{}
		if err := Unmarshal([]byte(`a:2:{i:1;s:1:"x";i:1;s:1:"y";}`), &got); err != nil {
			t.Fatal(err)
		}
		if want := map[uint64]string{1: "y"}; !reflect.DeepEqual(got, want) {
			t.Errorf("got %v", got)
		}
	})

	t.Run("非連続キーも map なら成功", func(t *testing.T) {
		got := map[int64]string{}
		if err := Unmarshal([]byte(`a:2:{i:0;s:1:"a";i:5;s:1:"b";}`), &got); err != nil {
			t.Fatal(err)
		}
		if want := map[int64]string{0: "a", 5: "b"}; !reflect.DeepEqual(got, want) {
			t.Errorf("got %v", got)
		}
	})
}

func TestUnmarshalStruct(t *testing.T) {
	// PHP: serialize() した WP の attachment metadata 相当
	full := `a:5:{s:5:"width";i:1200;s:6:"height";i:630;s:4:"file";s:19:"2024/01/example.jpg";s:8:"filesize";i:123456;s:5:"sizes";a:1:{s:9:"thumbnail";a:5:{s:4:"file";s:11:"example.jpg";s:5:"width";i:150;s:6:"height";i:150;s:9:"mime_type";s:10:"image/jpeg";s:8:"filesize";i:5000;}}}`

	t.Run("ネスト struct + map + ポインタ", func(t *testing.T) {
		var got attachmentMeta
		if err := Unmarshal([]byte(full), &got); err != nil {
			t.Fatal(err)
		}
		if got.Width != 1200 || got.Height != 630 || got.File != "2024/01/example.jpg" {
			t.Errorf("scalar fields: %+v", got)
		}
		if got.Filesize == nil || *got.Filesize != 123456 {
			t.Errorf("Filesize = %v", got.Filesize)
		}
		thumb, ok := got.Sizes["thumbnail"]
		if !ok || thumb.Width != 150 || thumb.MimeType != "image/jpeg" {
			t.Errorf("Sizes = %+v", got.Sizes)
		}
	})

	t.Run("未知キーはスキップ・欠けたキーはゼロ値のまま", func(t *testing.T) {
		var got attachmentMeta
		if err := Unmarshal([]byte(`a:2:{s:5:"width";i:800;s:7:"unknown";a:1:{i:0;s:1:"x";}}`), &got); err != nil {
			t.Fatal(err)
		}
		if got.Width != 800 || got.File != "" || got.Filesize != nil {
			t.Errorf("got %+v", got)
		}
	})

	t.Run("PHP object のプロパティ名マングリング", func(t *testing.T) {
		// PHP: class P { public $pub=1; protected $pro=2; private $pri=3; }
		in := "O:1:\"P\":3:{s:3:\"pub\";i:1;s:6:\"\x00*\x00pro\";i:2;s:6:\"\x00P\x00pri\";i:3;}"
		var got struct {
			Pub int `php:"pub"`
			Pro int `php:"pro"`
			Pri int `php:"pri"`
		}
		if err := Unmarshal([]byte(in), &got); err != nil {
			t.Fatal(err)
		}
		if got.Pub != 1 || got.Pro != 2 || got.Pri != 3 {
			t.Errorf("got %+v", got)
		}
	})

	t.Run("フィールド名の大文字小文字フォールバック", func(t *testing.T) {
		var got struct{ Width int } // タグなし
		if err := Unmarshal([]byte(`a:1:{s:5:"width";i:9;}`), &got); err != nil {
			t.Fatal(err)
		}
		if got.Width != 9 {
			t.Errorf("got %+v", got)
		}
	})

	t.Run("埋め込み struct のフラット化", func(t *testing.T) {
		type Base struct {
			ID int `php:"id"`
		}
		var got struct {
			Base
			Name string `php:"name"`
		}
		if err := Unmarshal([]byte(`a:2:{s:2:"id";i:7;s:4:"name";s:1:"x";}`), &got); err != nil {
			t.Fatal(err)
		}
		if got.ID != 7 || got.Name != "x" {
			t.Errorf("got %+v", got)
		}
	})
}

func TestUnmarshalAny(t *testing.T) {
	t.Run("list → []any", func(t *testing.T) {
		var got any
		if err := Unmarshal([]byte(`a:2:{i:0;s:1:"a";i:1;i:5;}`), &got); err != nil {
			t.Fatal(err)
		}
		if want := []any{"a", int64(5)}; !reflect.DeepEqual(got, want) {
			t.Errorf("got %#v", got)
		}
	})

	t.Run("連想配列 → map[string]any", func(t *testing.T) {
		var got any
		if err := Unmarshal([]byte(`a:2:{s:1:"k";i:1;i:2;s:1:"v";}`), &got); err != nil {
			t.Fatal(err)
		}
		if want := map[string]any{"k": int64(1), "2": "v"}; !reflect.DeepEqual(got, want) {
			t.Errorf("got %#v", got)
		}
	})

	t.Run("非連続キー → map (panic しない)", func(t *testing.T) {
		var got any
		if err := Unmarshal([]byte(`a:2:{i:0;s:1:"a";i:5;s:1:"b";}`), &got); err != nil {
			t.Fatal(err)
		}
		if want := map[string]any{"0": "a", "5": "b"}; !reflect.DeepEqual(got, want) {
			t.Errorf("got %#v", got)
		}
	})

	t.Run("object → map[string]any", func(t *testing.T) {
		var got any
		if err := Unmarshal([]byte(`O:8:"stdClass":1:{s:5:"width";i:10;}`), &got); err != nil {
			t.Fatal(err)
		}
		if want := map[string]any{"width": int64(10)}; !reflect.DeepEqual(got, want) {
			t.Errorf("got %#v", got)
		}
	})
}

func TestUnmarshalUnsupportedAndErrors(t *testing.T) {
	t.Run("参照 R: は ErrUnsupported", func(t *testing.T) {
		var got any
		err := Unmarshal([]byte(`a:2:{s:2:"r1";a:1:{s:1:"x";i:1;}s:2:"r2";R:2;}`), &got)
		if !errors.Is(err, ErrUnsupported) {
			t.Errorf("got %v", err)
		}
	})

	t.Run("enum E: は ErrUnsupported", func(t *testing.T) {
		var got any
		if err := Unmarshal([]byte(`E:11:"Suit:Hearts";`), &got); !errors.Is(err, ErrUnsupported) {
			t.Errorf("got %v", err)
		}
	})

	t.Run("未知キーの値の r: は skip できる", func(t *testing.T) {
		var got struct {
			Name string `php:"name"`
		}
		in := `a:2:{s:3:"ref";R:2;s:4:"name";s:1:"x";}`
		if err := Unmarshal([]byte(in), &got); err != nil || got.Name != "x" {
			t.Errorf("got %+v, %v", got, err)
		}
	})

	t.Run("個数不一致はエラー (PHP と同じ)", func(t *testing.T) {
		var got any
		if err := Unmarshal([]byte(`a:3:{i:0;s:1:"a";}`), &got); err == nil {
			t.Error("エラーにならない")
		}
	})

	t.Run("末尾ゴミは既定でエラー・オプションで許容", func(t *testing.T) {
		var n int64
		err := Unmarshal([]byte(`i:1;garbage`), &n)
		if !errors.Is(err, ErrTrailingData) {
			t.Errorf("got %v", err)
		}
		dec := NewDecoder(WithAllowTrailingData())
		if err := dec.Unmarshal([]byte(`i:1;garbage`), &n); err != nil || n != 1 {
			t.Errorf("got %v, %v", n, err)
		}
	})

	t.Run("深さ上限", func(t *testing.T) {
		deep := ""
		for range 20 {
			deep += `a:1:{i:0;`
		}
		deep += `N;`
		for range 20 {
			deep += `}`
		}
		var got any
		dec := NewDecoder(WithMaxDepth(10))
		if err := dec.Unmarshal([]byte(deep), &got); !errors.Is(err, ErrDepth) {
			t.Errorf("got %v", err)
		}
		if err := Unmarshal([]byte(deep), &got); err != nil {
			t.Errorf("既定の深さで失敗: %v", err)
		}
	})

	t.Run("MaxInt 付近の長さ宣言は加算オーバーフローせずエラー", func(t *testing.T) {
		// 加算形の境界チェック (s.off+n) だと n が MaxInt 付近でラップして
		// チェックをすり抜け、slice bounds panic になる (CodeRabbit 指摘の回帰テスト)
		for _, in := range []string{
			`s:9223372036854775807:"a";`,
			`S:9223372036854775807:"a";`,
			`O:9223372036854775807:"P":1:{s:1:"a";i:1;}`,
			`a:1:{s:3:"foo";E:9223372036854775807:"X:Y";}`,
			`a:1:{s:3:"foo";C:9223372036854775807:"X":1:{a}}`,
			`a:1:{s:3:"foo";C:1:"X":9223372036854775807:{a}}`,
		} {
			var got any
			if err := Unmarshal([]byte(in), &got); err == nil {
				t.Errorf("%s がエラーにならない", in)
			}
		}
	})

	t.Run("巨大 count 宣言は即エラー (DoS 防止)", func(t *testing.T) {
		var got []string
		err := Unmarshal([]byte(`a:99999999:{i:0;s:1:"a";}`), &got)
		var se *SyntaxError
		if !errors.As(err, &se) {
			t.Errorf("got %v", err)
		}
	})

	t.Run("不正ターゲット", func(t *testing.T) {
		var n int
		if err := Unmarshal([]byte(`i:1;`), n); !errors.Is(err, ErrInvalidTarget) {
			t.Errorf("got %v", err)
		}
		if err := Unmarshal([]byte(`i:1;`), nil); !errors.Is(err, ErrInvalidTarget) {
			t.Errorf("got %v", err)
		}
	})

	t.Run("SyntaxError はオフセットを持つ", func(t *testing.T) {
		var n int64
		err := Unmarshal([]byte(`i:xx;`), &n)
		var se *SyntaxError
		if !errors.As(err, &se) || se.Offset != 2 {
			t.Errorf("got %v", err)
		}
	})
}

func TestUnmarshalWeakTypes(t *testing.T) {
	dec := NewDecoder(WithWeakTypes())

	t.Run("数値文字列 → int (WP メタの実データ形)", func(t *testing.T) {
		var got struct {
			Width int `php:"width"`
		}
		if err := dec.Unmarshal([]byte(`a:1:{s:5:"width";s:4:"1200";}`), &got); err != nil || got.Width != 1200 {
			t.Errorf("got %+v, %v", got, err)
		}
		// 既定 (厳密) ではエラー
		if err := Unmarshal([]byte(`a:1:{s:5:"width";s:4:"1200";}`), &got); err == nil {
			t.Error("厳密モードでエラーにならない")
		}
	})

	t.Run("int → string / bool", func(t *testing.T) {
		var s string
		if err := dec.Unmarshal([]byte(`i:42;`), &s); err != nil || s != "42" {
			t.Errorf("got %q, %v", s, err)
		}
		var b bool
		if err := dec.Unmarshal([]byte(`i:0;`), &b); err != nil || b {
			t.Errorf("got %v, %v", b, err)
		}
		// PHP の真偽値キャスト: "0" は false
		if err := dec.Unmarshal([]byte(`s:1:"0";`), &b); err != nil || b {
			t.Errorf("got %v, %v", b, err)
		}
		if err := dec.Unmarshal([]byte(`s:1:"x";`), &b); err != nil || !b {
			t.Errorf("got %v, %v", b, err)
		}
	})
}

type customUnmarshal struct {
	raw string
}

func (c *customUnmarshal) UnmarshalPHP(data []byte) error {
	c.raw = string(data)
	return nil
}

func TestUnmarshaler(t *testing.T) {
	var got struct {
		V customUnmarshal `php:"v"`
	}
	if err := Unmarshal([]byte(`a:1:{s:1:"v";a:1:{i:0;i:9;}}`), &got); err != nil {
		t.Fatal(err)
	}
	if got.V.raw != `a:1:{i:0;i:9;}` {
		t.Errorf("got %q", got.V.raw)
	}
}
