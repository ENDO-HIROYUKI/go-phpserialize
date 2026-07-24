// Package phpserialize は PHP の serialize() / unserialize() 互換フォーマットの
// Marshal / Unmarshal を提供する。
//
// 設計原則:
//   - どんな入力でも panic しない (fuzz で検証)
//   - unsafe / go:linkname を使わない (Go のバージョンアップに追従できる実装だけで構成する)
//   - 仕様は PHP 本体 (ext/standard/var.c / var_unserializer.re) の実挙動に合わせ、
//     意図的な相違点はドキュメントに明記する
package phpserialize

// DefaultMaxDepth は入れ子の深さの既定上限。PHP の unserialize_max_depth の既定値と同じ。
const DefaultMaxDepth = 4096

type config struct {
	maxDepth           int
	weakTypes          bool
	allowTrailing      bool
	sparseArrayPadding bool
}

func defaultConfig() config {
	return config{maxDepth: DefaultMaxDepth}
}

// Option は Decoder / Encoder の動作を調整する。
type Option func(*config)

// WithMaxDepth は入れ子の深さの上限を変更する (0 以下は無視)。
func WithMaxDepth(n int) Option {
	return func(c *config) {
		if n > 0 {
			c.maxDepth = n
		}
	}
}

// WithWeakTypes は PHP 的な弱い型変換 (数値文字列 → 数値、数値 → bool 等) を許可する。
// WordPress のメタデータのように数値が文字列で保存されているデータのデコードに使う。
func WithWeakTypes() Option {
	return func(c *config) { c.weakTypes = true }
}

// WithSparseArrayPadding は trim21/go-phpserialize v0.0.x 互換の疎配列を扱うために使う。
func WithSparseArrayPadding() Option {
	return func(c *config) { c.sparseArrayPadding = true }
}

// WithAllowTrailingData は値の後に余分なバイトが残っていてもエラーにしない。
// (PHP の unserialize() は警告を出しつつ成功する。本ライブラリの既定は厳密エラー。)
func WithAllowTrailingData() Option {
	return func(c *config) { c.allowTrailing = true }
}

// Decoder はオプションを保持する再利用可能なデコーダ。ゴルーチンセーフ。
type Decoder struct {
	cfg config
}

// NewDecoder はオプション付きの Decoder を作る。
func NewDecoder(opts ...Option) *Decoder {
	d := &Decoder{cfg: defaultConfig()}
	for _, o := range opts {
		o(&d.cfg)
	}
	return d
}

// Encoder はオプションを保持する再利用可能なエンコーダ。ゴルーチンセーフ。
type Encoder struct {
	cfg config
}

// NewEncoder はオプション付きの Encoder を作る。
func NewEncoder(opts ...Option) *Encoder {
	e := &Encoder{cfg: defaultConfig()}
	for _, o := range opts {
		o(&e.cfg)
	}
	return e
}

// Marshaler を実装した型は自身のシリアライズ表現を返せる。
// 返り値は完全な PHP シリアライズ値 1 個でなければならない。
type Marshaler interface {
	MarshalPHP() ([]byte, error)
}

// Unmarshaler を実装した型は自身でデコードを行える。
// data には値 1 個分のシリアライズ表現が渡される。
type Unmarshaler interface {
	UnmarshalPHP(data []byte) error
}

var (
	defaultDecoder = NewDecoder()
	defaultEncoder = NewEncoder()
)

// Unmarshal は data を PHP シリアライズ形式としてデコードし v へ書き込む。
func Unmarshal(data []byte, v any) error {
	return defaultDecoder.Unmarshal(data, v)
}

// Marshal は v を PHP シリアライズ形式にエンコードする。
func Marshal(v any) ([]byte, error) {
	return defaultEncoder.Marshal(v)
}
