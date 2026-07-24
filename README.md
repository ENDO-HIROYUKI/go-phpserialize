# go-phpserialize

PHP の `serialize()` / `unserialize()` 互換フォーマットの Marshal / Unmarshal を提供する Go ライブラリ。

```bash
go get github.com/ENDO-HIROYUKI/go-phpserialize
```

```go
import "github.com/ENDO-HIROYUKI/go-phpserialize"

type Meta struct {
    Width  int    `php:"width"`
    File   string `php:"file"`
}

var m Meta
err := phpserialize.Unmarshal([]byte(`a:2:{s:5:"width";i:1200;s:4:"file";s:5:"a.jpg";}`), &m)

b, err := phpserialize.Marshal(m) // a:2:{s:5:"width";i:1200;s:4:"file";s:5:"a.jpg";}
```

## なぜ作ったか

[trim21/go-phpserialize](https://github.com/trim21/go-phpserialize) には次の問題があった。

- **不正な形の入力で panic する** — 非連続キーの配列を slice へデコードすると `reflect: slice index out of range` でクラッシュする ([trim21/go-phpserialize#79](https://github.com/trim21/go-phpserialize/issues/79))。WordPress のメタデータなど実データでこの形は普通に発生する。
- **`go:linkname` / `unsafe` に依存** — v0.0.x は Go 内部 API に依存していたため、Go 1.23 以降でビルド自体を拒否するようになった。

本ライブラリの設計原則:

1. **どんな入力でも panic しない。** 2 種類の fuzz (no-panic / round-trip) で継続的に検証する。
2. **`unsafe` / `go:linkname` を使わない。** 純 reflect + 型ごとのプランキャッシュのみで実装し、Go のバージョンアップに追従できる状態を保つ。
3. **仕様は PHP 本体の実挙動に合わせる。** ext/standard/var.c / var_unserializer.re の挙動を PHP 8.4 で実測し、テストベクタ (`testdata/gen.php`) と実 PHP との差分テストで担保する。意図的な相違点は下表に明記する。

## 対応フォーマット

| トークン | Unmarshal | Marshal |
| --- | --- | --- |
| `N;` (null) | ✅ ゼロ値 / nil を設定 | ✅ nil ポインタ等 |
| `b:` (bool) | ✅ | ✅ |
| `i:` (int) | ✅ | ✅ |
| `d:` (float, `INF` / `-INF` / `NAN` 含む) | ✅ | ✅ 最短の往復可能表現 (PHP `serialize_precision=-1` 相当) |
| `s:` (string, バイト長) | ✅ | ✅ |
| `S:` (hex エスケープ文字列, deprecated) | ✅ | 出力しない |
| `a:` (array) | ✅ slice / array / map / struct / any | ✅ |
| `O:` (object) | ✅ struct / map へ (プロパティ名のマングリング `\0Class\0` / `\0*\0` を正規化) | ❌ struct は `a:` として出力 |
| `r:` `R:` (参照) / `C:` / `E:` (enum) | ❌ `ErrUnsupported` (未知キーの値としては読み飛ばし可) | 出力しない |

## PHP との意図的な相違

| 項目 | PHP | 本ライブラリ |
| --- | --- | --- |
| int64 範囲外の `i:` | PHP_INT_MAX にクランプ + 警告 | エラー |
| 末尾の余分なデータ | 警告付きで成功 | 既定はエラー。`WithAllowTrailingData()` で許容 |
| 配列の挿入順 | 保持する | Go の map では保持できないため、Marshal はキーをソートして決定的に出力 (int 昇順 → string バイト順) |
| 非連続キー配列 → slice | (PHP に slice の概念はない) | キー集合が `{0..n-1}` のときだけ成功、それ以外はエラー |

PHP に忠実な点: 文字列はバイト長で扱う (マルチバイト安全) / 数値文字列キーは int に正規化 / 重複キーは後勝ち / 要素数の不一致はエラー / 深さ上限は既定 4096 (`unserialize_max_depth` の既定と同じ)。

## オプション

```go
dec := phpserialize.NewDecoder(
    phpserialize.WithWeakTypes(),          // PHP 的な弱い型変換 (例: s:"1200" → int)。WP メタ向け
    phpserialize.WithSparseArrayPadding(), // 疎配列を最大キーまでゼロ値で埋めて slice にデコード
    phpserialize.WithAllowTrailingData(),  // 末尾ゴミを許容 (PHP の実挙動に相当)
    phpserialize.WithMaxDepth(4096),
)
err := dec.Unmarshal(data, &v)
```

### 疎配列の slice デコード

`WithSparseArrayPadding()` を指定すると、非負整数キーまたは正準形の数値文字列キーを持つ PHP 配列を、最大キーまでゼロ値で埋めた slice にデコードする。重複キーは PHP と同じく入力順の後勝ちになる。復元後の長さは最大 `1 << 20` で、負キーと上限以上のキーはエラーになる。`[]byte` と固定長配列には適用されない。

`Marshaler` / `Unmarshaler` インターフェースで型ごとのカスタム表現も定義できる。

既知の制限: **非公開の匿名 (埋め込み) struct のフィールドは昇格しない** (encoding/json は marshal 側のみ昇格させるが、本ライブラリは reflect の read-only 値経由の panic リスクを避けるため Marshal / Unmarshal とも対称にスキップする)。

## 性能

WordPress の `_wp_attachment_metadata` 相当のデコード (Apple M1 Pro):

```
BenchmarkUnmarshalStruct       941.9 ns/op
BenchmarkUnmarshalStringSlice  511.1 ns/op
BenchmarkMarshalStruct         544.0 ns/op
```

unsafe ベースの trim21 v0.1.2 比で約 1.7 倍のコスト。安全性と引き換えとして許容している (正しさ優先)。

## セキュリティ

- 宣言された配列個数・文字列長は残り入力サイズと突き合わせて検証する (巨大な長さ宣言によるメモリ確保 DoS を防止)
- 入れ子の深さは `MaxDepth` で制限
- fuzz corpus に発見済みの問題入力を回帰テストとして保持

## License

MIT
