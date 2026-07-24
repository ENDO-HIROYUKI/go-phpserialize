package phpserialize

import (
	"fmt"
	"math"
	"reflect"
	"strings"
	"sync"
	"testing"
)

// 期待値は PHP 8.4.21 の serialize() 実出力に合わせている。

func TestMarshalScalars(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   any
		want string
	}{
		{"nil", nil, `N;`},
		{"true", true, `b:1;`},
		{"false", false, `b:0;`},
		{"int", 123, `i:123;`},
		{"negative int", -123, `i:-123;`},
		{"int64 min", int64(math.MinInt64), `i:-9223372036854775808;`},
		{"uint", uint(42), `i:42;`},
		{"float 0.1", 0.1, `d:0.1;`},
		{"float 1/3", 1.0 / 3.0, `d:0.3333333333333333;`},
		{"float 整数値", 1.0, `d:1;`},
		{"INF", math.Inf(1), `d:INF;`},
		{"-INF", math.Inf(-1), `d:-INF;`},
		{"NAN", math.NaN(), `d:NAN;`},
		{"string", "abc", `s:3:"abc";`},
		{"マルチバイトはバイト長", "プロレス", `s:12:"プロレス";`},
		{"空文字列", "", `s:0:"";`},
		{"[]byte", []byte("ab"), `s:2:"ab";`},
		{"nil slice", []string(nil), `N;`},
		{"nil map", map[string]int(nil), `N;`},
		{"ポインタ deref", ptr(7), `i:7;`},
		{"nil ポインタ", (*int)(nil), `N;`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Marshal(tc.in)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != tc.want {
				t.Errorf("got %s, want %s", got, tc.want)
			}
		})
	}
}

func ptr[T any](v T) *T { return &v }

func TestMarshalComposite(t *testing.T) {
	t.Run("slice", func(t *testing.T) {
		got, err := Marshal([]string{"a", "b"})
		if err != nil {
			t.Fatal(err)
		}
		if want := `a:2:{i:0;s:1:"a";i:1;s:1:"b";}`; string(got) != want {
			t.Errorf("got %s", got)
		}
	})

	t.Run("map は決定的順序 (int 昇順)", func(t *testing.T) {
		got, err := Marshal(map[int]string{5: "b", 1: "a"})
		if err != nil {
			t.Fatal(err)
		}
		if want := `a:2:{i:1;s:1:"a";i:5;s:1:"b";}`; string(got) != want {
			t.Errorf("got %s", got)
		}
	})

	t.Run("map string キーはバイト順・数値文字列キーは int へ正規化 (PHP と同じ)", func(t *testing.T) {
		got, err := Marshal(map[string]int{"b": 2, "a": 1, "5": 3})
		if err != nil {
			t.Fatal(err)
		}
		// ソートは元のキー文字列 ("5" < "a" < "b")、"5" は i:5 に正規化
		if want := `a:3:{i:5;i:3;s:1:"a";i:1;s:1:"b";i:2;}`; string(got) != want {
			t.Errorf("got %s", got)
		}
	})

	t.Run("正規化されない数値風キー", func(t *testing.T) {
		got, err := Marshal(map[string]int{"05": 1})
		if err != nil {
			t.Fatal(err)
		}
		if want := `a:1:{s:2:"05";i:1;}`; string(got) != want {
			t.Errorf("got %s", got)
		}
	})

	t.Run("struct は php タグ + 宣言順", func(t *testing.T) {
		in := struct {
			Width int    `php:"width"`
			File  string `php:"file"`
			Skip  string `php:"-"`
			NoTag int
		}{Width: 10, File: "x.jpg", Skip: "no", NoTag: 5}
		got, err := Marshal(in)
		if err != nil {
			t.Fatal(err)
		}
		if want := `a:3:{s:5:"width";i:10;s:4:"file";s:5:"x.jpg";s:5:"NoTag";i:5;}`; string(got) != want {
			t.Errorf("got %s", got)
		}
	})

	t.Run("omitempty", func(t *testing.T) {
		in := struct {
			A string `php:"a,omitempty"`
			B int    `php:"b,omitempty"`
			C *int   `php:"c,omitempty"`
			D []int  `php:"d,omitempty"`
			E string `php:"e"`
		}{}
		got, err := Marshal(in)
		if err != nil {
			t.Fatal(err)
		}
		if want := `a:1:{s:1:"e";s:0:"";}`; string(got) != want {
			t.Errorf("got %s", got)
		}
	})

	t.Run("埋め込み struct のフラット化", func(t *testing.T) {
		type Base struct {
			ID int `php:"id"`
		}
		in := struct {
			Base
			Name string `php:"name"`
		}{Base{7}, "x"}
		got, err := Marshal(in)
		if err != nil {
			t.Fatal(err)
		}
		if want := `a:2:{s:2:"id";i:7;s:4:"name";s:1:"x";}`; string(got) != want {
			t.Errorf("got %s", got)
		}
	})

	t.Run("ネスト", func(t *testing.T) {
		in := map[string][]int{"xs": {1, 2}}
		got, err := Marshal(in)
		if err != nil {
			t.Fatal(err)
		}
		if want := `a:1:{s:2:"xs";a:2:{i:0;i:1;i:1;i:2;}}`; string(got) != want {
			t.Errorf("got %s", got)
		}
	})
}

func TestMarshalErrors(t *testing.T) {
	t.Run("uint オーバーフロー", func(t *testing.T) {
		if _, err := Marshal(uint64(math.MaxUint64)); err == nil {
			t.Error("エラーにならない")
		}
	})

	t.Run("循環参照は深度ガードで止まる", func(t *testing.T) {
		type node struct {
			Next *node `php:"next"`
		}
		n := &node{}
		n.Next = n
		if _, err := Marshal(n); err == nil {
			t.Error("エラーにならない")
		}
	})

	t.Run("未対応の型", func(t *testing.T) {
		if _, err := Marshal(make(chan int)); err == nil {
			t.Error("chan がエラーにならない")
		}
		if _, err := Marshal(func() {}); err == nil {
			t.Error("func がエラーにならない")
		}
	})
}

type customMarshal struct{ v int }

func (c customMarshal) MarshalPHP() ([]byte, error) {
	return []byte("i:" + string(rune('0'+c.v)) + ";"), nil
}

func TestMarshaler(t *testing.T) {
	got, err := Marshal(map[string]customMarshal{"k": {7}})
	if err != nil {
		t.Fatal(err)
	}
	if want := `a:1:{s:1:"k";i:7;}`; string(got) != want {
		t.Errorf("got %s", got)
	}
}

func TestRoundTrip(t *testing.T) {
	// Marshal → Unmarshal で元の値へ戻ることを型付きで確認する
	t.Run("struct", func(t *testing.T) {
		size := 5000
		in := attachmentMeta{
			Width: 1200, Height: 630, File: "2024/01/エグザンプル.jpg",
			Filesize: ptr(123456),
			Sizes: map[string]attachmentSize{
				"thumbnail": {File: "t.jpg", Width: 150, Height: 150, MimeType: "image/jpeg", Filesize: &size},
			},
		}
		b, err := Marshal(in)
		if err != nil {
			t.Fatal(err)
		}
		var got attachmentMeta
		if err := Unmarshal(b, &got); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(in, got) {
			t.Errorf("round trip mismatch:\n in  %+v\n got %+v", in, got)
		}
	})

	t.Run("any", func(t *testing.T) {
		in := map[string]any{
			"list": []any{int64(1), "a", true, nil},
			"num":  0.5,
			"5":    int64(9), // 数値文字列キーは i:5 → 復元時も "5"
		}
		b, err := Marshal(in)
		if err != nil {
			t.Fatal(err)
		}
		var got any
		if err := Unmarshal(b, &got); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(in, map[string]any(got.(map[string]any))) {
			t.Errorf("round trip mismatch:\n in  %#v\n got %#v", in, got)
		}
	})
}

// Marshal はエンコードバッファを sync.Pool で再利用するため、返り値の所有権が
// 呼び出し側に完全に渡ることを固定する (#6)。
func TestMarshalBufferOwnership(t *testing.T) {
	t.Run("返り値は後続の Marshal で不変", func(t *testing.T) {
		first, err := Marshal([]string{"hello", "world"})
		if err != nil {
			t.Fatal(err)
		}
		want := string(first)
		// 異なるサイズの Marshal を挟んでも first が書き換わらないこと
		if _, err := Marshal(strings.Repeat("x", 300)); err != nil {
			t.Fatal(err)
		}
		if _, err := Marshal(int64(1)); err != nil {
			t.Fatal(err)
		}
		if string(first) != want {
			t.Errorf("先行の返り値が破壊された: %q", first)
		}
	})

	t.Run("返り値を書き換えても後続に影響しない", func(t *testing.T) {
		b1, err := Marshal(int64(42))
		if err != nil {
			t.Fatal(err)
		}
		for i := range b1 {
			b1[i] = '!'
		}
		b2, err := Marshal(int64(42))
		if err != nil {
			t.Fatal(err)
		}
		if want := "i:42;"; string(b2) != want {
			t.Errorf("got %q, want %q", b2, want)
		}
	})

	t.Run("エラー後の Marshal は正常成功する", func(t *testing.T) {
		enc := NewEncoder(WithMaxDepth(1))
		if _, err := enc.Marshal([][]int64{{1}}); err == nil {
			t.Fatal("深さ超過がエラーにならない")
		}
		b, err := enc.Marshal(int64(7))
		if err != nil {
			t.Fatalf("エラー後の Marshal が失敗: %v", err)
		}
		if want := "i:7;"; string(b) != want {
			t.Errorf("got %q, want %q", b, want)
		}
	})

	t.Run("巨大な結果の後も正常動作する", func(t *testing.T) {
		// プール容量上限 (256 KiB) を超えるバッファは再利用されない。挙動が変わらないことだけ確認する。
		big, err := Marshal(strings.Repeat("x", 512<<10))
		if err != nil {
			t.Fatal(err)
		}
		if len(big) < 512<<10 {
			t.Errorf("巨大 Marshal の結果が短すぎる: %d", len(big))
		}
		b, err := Marshal(int64(1))
		if err != nil || string(b) != "i:1;" {
			t.Errorf("got %q, %v", b, err)
		}
	})
}

// 同一 Encoder の並行 Marshal で返り値が混線しないこと (-race で検証)。
func TestMarshalConcurrent(t *testing.T) {
	enc := NewEncoder()
	failures := make(chan string, 16)
	var wg sync.WaitGroup
	for g := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			want := fmt.Sprintf("s:%d:%q;", len(fmt.Sprint(g))+7, fmt.Sprintf("worker-%d", g))
			var results [][]byte
			for range 50 {
				b, err := enc.Marshal(fmt.Sprintf("worker-%d", g))
				if err != nil {
					failures <- err.Error()
					return
				}
				results = append(results, b)
			}
			// 全 goroutine 終了後の検証に備えて最後にまとめて確認する
			for _, b := range results {
				if string(b) != want {
					failures <- fmt.Sprintf("got %q, want %q", b, want)
					return
				}
			}
		}()
	}
	wg.Wait()
	close(failures)
	for f := range failures {
		t.Error(f)
	}
}
