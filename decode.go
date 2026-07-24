package phpserialize

import (
	"fmt"
	"math"
	"reflect"
	"strconv"
)

// Unmarshal は data をデコードして v へ書き込む。v は nil でないポインタであること。
func (d *Decoder) Unmarshal(data []byte, v any) error {
	rv := reflect.ValueOf(v)
	if rv.Kind() != reflect.Pointer || rv.IsNil() {
		return fmt.Errorf("%w (got %T)", ErrInvalidTarget, v)
	}
	plan, err := decPlanFor(rv.Type().Elem())
	if err != nil {
		return err
	}
	s := &decodeState{data: data, cfg: &d.cfg, sparsePadRemaining: d.cfg.sparsePaddingBudget}
	if err := plan(s, rv.Elem()); err != nil {
		return err
	}
	if s.off != len(s.data) && !d.cfg.allowTrailing {
		return fmt.Errorf("%w at offset %d", ErrTrailingData, s.off)
	}
	return nil
}

type decodeState struct {
	data  []byte
	off   int
	depth int
	cfg   *config
	// sparsePadRemaining は疎配列パディングの残りバジェット (バイト)。デコード 1 回ごとにリセットされる。
	sparsePadRemaining int
}

func (s *decodeState) syntaxErr(off int, format string, args ...any) error {
	return &SyntaxError{Offset: off, Msg: fmt.Sprintf(format, args...)}
}

func (s *decodeState) errUnexpectedEnd() error {
	return s.syntaxErr(len(s.data), "unexpected end of data")
}

// peek は現在位置のバイトを消費せず返す。
func (s *decodeState) peek() (byte, error) {
	if s.off >= len(s.data) {
		return 0, s.errUnexpectedEnd()
	}
	return s.data[s.off], nil
}

// expect は現在位置のバイトが c であることを検証して 1 バイト進める。
func (s *decodeState) expect(c byte) error {
	b, err := s.peek()
	if err != nil {
		return err
	}
	if b != c {
		return s.syntaxErr(s.off, "expected %q, got %q", c, b)
	}
	s.off++
	return nil
}

// enter / leave は入れ子の深さを数える。
func (s *decodeState) enter() error {
	s.depth++
	if s.depth > s.cfg.maxDepth {
		return ErrDepth
	}
	return nil
}

func (s *decodeState) leave() { s.depth-- }

// chargeSparsePadding は疎配列のゼロ埋めパディング (pad 要素 × esize バイト) を
// 残りバジェットにチャージし、超過なら ErrSparsePaddingBudget を返す。
func (s *decodeState) chargeSparsePadding(off, pad int, esize uintptr) error {
	if pad <= 0 {
		return nil
	}
	// 乗算のオーバーフローを避けるため除算で超過を判定する
	if uintptr(pad) > uintptr(s.sparsePadRemaining)/esize {
		// 積 (pad × esize) は int を超え得るため計算せず、要素数と要素サイズを個別に表示する
		return fmt.Errorf("%w at offset %d: padding %d elements x %d bytes/element exceeds remaining %d bytes",
			ErrSparsePaddingBudget, off, pad, esize, s.sparsePadRemaining)
	}
	s.sparsePadRemaining -= int(uintptr(pad) * esize)
	return nil
}

// parseIntBody は符号付き 10 進整数を読み取る (終端記号は消費しない)。
// int64 の範囲外はエラー (PHP は PHP_INT_MAX へクランプ+警告するが、本ライブラリは厳密)。
func (s *decodeState) parseIntBody() (int64, error) {
	start := s.off
	neg := false
	if b, err := s.peek(); err != nil {
		return 0, err
	} else if b == '-' {
		neg = true
		s.off++
	}
	var n uint64
	nd := 0
	for s.off < len(s.data) {
		c := s.data[s.off]
		if c < '0' || c > '9' {
			break
		}
		d := uint64(c - '0')
		if n > (math.MaxUint64-d)/10 {
			return 0, s.syntaxErr(start, "integer out of range")
		}
		n = n*10 + d
		nd++
		s.off++
	}
	if nd == 0 {
		return 0, s.syntaxErr(s.off, "expected digits")
	}
	if neg {
		if n > 1<<63 {
			return 0, s.syntaxErr(start, "integer out of range")
		}
		return -int64(n-1) - 1, nil // -n を 2 の補数の下限まで安全に表現する
	}
	if n > math.MaxInt64 {
		return 0, s.syntaxErr(start, "integer out of range")
	}
	return int64(n), nil
}

// parseLen は非負の長さ・個数を読み取る (終端記号は消費しない)。
func (s *decodeState) parseLen() (int, error) {
	start := s.off
	var n int
	nd := 0
	for s.off < len(s.data) {
		c := s.data[s.off]
		if c < '0' || c > '9' {
			break
		}
		if n > (math.MaxInt-int(c-'0'))/10 {
			return 0, s.syntaxErr(start, "length out of range")
		}
		n = n*10 + int(c-'0')
		nd++
		s.off++
	}
	if nd == 0 {
		return 0, s.syntaxErr(s.off, "expected length digits")
	}
	return n, nil
}

// --- 各トークンの完全形パーサ (先頭のタグ文字から終端まで消費する) ---

func (s *decodeState) parseNull() error {
	if err := s.expect('N'); err != nil {
		return err
	}
	return s.expect(';')
}

func (s *decodeState) parseBool() (bool, error) {
	if err := s.expect('b'); err != nil {
		return false, err
	}
	if err := s.expect(':'); err != nil {
		return false, err
	}
	b, err := s.peek()
	if err != nil {
		return false, err
	}
	// PHP と同様 0 / 1 のみ許可 (b:2; はエラー)
	if b != '0' && b != '1' {
		return false, s.syntaxErr(s.off, "bool value must be 0 or 1, got %q", b)
	}
	s.off++
	return b == '1', s.expect(';')
}

func (s *decodeState) parseInt() (int64, error) {
	if err := s.expect('i'); err != nil {
		return 0, err
	}
	if err := s.expect(':'); err != nil {
		return 0, err
	}
	n, err := s.parseIntBody()
	if err != nil {
		return 0, err
	}
	return n, s.expect(';')
}

func (s *decodeState) parseFloat() (float64, error) {
	if err := s.expect('d'); err != nil {
		return 0, err
	}
	if err := s.expect(':'); err != nil {
		return 0, err
	}
	start := s.off
	for s.off < len(s.data) && s.data[s.off] != ';' {
		s.off++
	}
	if s.off >= len(s.data) {
		return 0, s.errUnexpectedEnd()
	}
	body := string(s.data[start:s.off])
	s.off++ // ';'
	switch body {
	case "INF":
		return math.Inf(1), nil
	case "-INF":
		return math.Inf(-1), nil
	case "NAN":
		return math.NaN(), nil
	}
	f, err := strconv.ParseFloat(body, 64)
	if err != nil {
		return 0, s.syntaxErr(start, "invalid float %q", body)
	}
	return f, nil
}

// parseStringBytes は s: / S: トークンを読み、文字列のバイト列を返す。
// s: の場合は入力のサブスライス (コピーなし)、S: の場合はエスケープ解決済みの新規スライスを返す。
func (s *decodeState) parseStringBytes() ([]byte, error) {
	tag, err := s.peek()
	if err != nil {
		return nil, err
	}
	switch tag {
	case 's':
		s.off++
		if err := s.expect(':'); err != nil {
			return nil, err
		}
		n, err := s.parseLen()
		if err != nil {
			return nil, err
		}
		if err := s.expect(':'); err != nil {
			return nil, err
		}
		if err := s.expect('"'); err != nil {
			return nil, err
		}
		if n > len(s.data)-s.off {
			return nil, s.syntaxErr(s.off, "string length %d exceeds input", n)
		}
		b := s.data[s.off : s.off+n]
		s.off += n
		if err := s.expect('"'); err != nil {
			return nil, err
		}
		return b, s.expect(';')
	case 'S':
		// PHP 8.x で deprecated の hex エスケープ形式。長さはデコード後のバイト数。
		s.off++
		if err := s.expect(':'); err != nil {
			return nil, err
		}
		n, err := s.parseLen()
		if err != nil {
			return nil, err
		}
		if err := s.expect(':'); err != nil {
			return nil, err
		}
		if err := s.expect('"'); err != nil {
			return nil, err
		}
		// デコード後 1 バイトは入力 1 バイト以上を消費するため、長さは残り入力を超えられない
		if n > len(s.data)-s.off {
			return nil, s.syntaxErr(s.off, "string length %d exceeds input", n)
		}
		out := make([]byte, 0, n)
		for len(out) < n {
			c, err := s.peek()
			if err != nil {
				return nil, err
			}
			if c != '\\' {
				out = append(out, c)
				s.off++
				continue
			}
			s.off++
			if s.off+2 > len(s.data) {
				return nil, s.errUnexpectedEnd()
			}
			hi := unhex(s.data[s.off])
			lo := unhex(s.data[s.off+1])
			if hi < 0 || lo < 0 {
				return nil, s.syntaxErr(s.off, "invalid hex escape")
			}
			out = append(out, byte(hi<<4|lo))
			s.off += 2
		}
		if err := s.expect('"'); err != nil {
			return nil, err
		}
		return out, s.expect(';')
	}
	return nil, s.syntaxErr(s.off, "expected string, got %q", tag)
}

func unhex(c byte) int {
	switch {
	case '0' <= c && c <= '9':
		return int(c - '0')
	case 'a' <= c && c <= 'f':
		return int(c-'a') + 10
	case 'A' <= c && c <= 'F':
		return int(c-'A') + 10
	}
	return -1
}

// parseArrayHeader は "a:COUNT:{" を読み、要素数を返す。
// 宣言された要素数が残り入力から物理的に不可能な場合は即エラーにする (巨大 count による DoS 防止)。
func (s *decodeState) parseArrayHeader() (int, error) {
	if err := s.expect('a'); err != nil {
		return 0, err
	}
	if err := s.expect(':'); err != nil {
		return 0, err
	}
	n, err := s.parseLen()
	if err != nil {
		return 0, err
	}
	if err := s.expect(':'); err != nil {
		return 0, err
	}
	if err := s.expect('{'); err != nil {
		return 0, err
	}
	// 最小のエントリは "i:0;N;" の 6 バイト
	if n > (len(s.data)-s.off)/6+1 {
		return 0, s.syntaxErr(s.off, "array count %d exceeds remaining input", n)
	}
	return n, nil
}

// parseObjectHeader は `O:LEN:"Class":COUNT:{` を読み、クラス名とプロパティ数を返す。
func (s *decodeState) parseObjectHeader() (string, int, error) {
	if err := s.expect('O'); err != nil {
		return "", 0, err
	}
	if err := s.expect(':'); err != nil {
		return "", 0, err
	}
	nameLen, err := s.parseLen()
	if err != nil {
		return "", 0, err
	}
	if err := s.expect(':'); err != nil {
		return "", 0, err
	}
	if err := s.expect('"'); err != nil {
		return "", 0, err
	}
	if nameLen > len(s.data)-s.off {
		return "", 0, s.syntaxErr(s.off, "class name length %d exceeds input", nameLen)
	}
	name := string(s.data[s.off : s.off+nameLen])
	s.off += nameLen
	if err := s.expect('"'); err != nil {
		return "", 0, err
	}
	if err := s.expect(':'); err != nil {
		return "", 0, err
	}
	n, err := s.parseLen()
	if err != nil {
		return "", 0, err
	}
	if err := s.expect(':'); err != nil {
		return "", 0, err
	}
	if err := s.expect('{'); err != nil {
		return "", 0, err
	}
	if n > (len(s.data)-s.off)/6+1 {
		return "", 0, s.syntaxErr(s.off, "property count %d exceeds remaining input", n)
	}
	return name, n, nil
}

// normalizePropName は PHP の private/protected プロパティ名のマングリングを取り除く。
// "\0*\0name" (protected) / "\0Class\0name" (private) → "name"
func normalizePropName(b []byte) []byte {
	if len(b) == 0 || b[0] != 0 {
		return b
	}
	for i := 1; i < len(b); i++ {
		if b[i] == 0 {
			return b[i+1:]
		}
	}
	return b
}

// skipValue は任意の値 1 個を検証しながら読み飛ばす。
// 未対応トークン (r:/R:/C:/E:) も構文としては読み飛ばせる (struct の未知キーの値などで使う)。
func (s *decodeState) skipValue() error {
	tag, err := s.peek()
	if err != nil {
		return err
	}
	switch tag {
	case 'N':
		return s.parseNull()
	case 'b':
		_, err := s.parseBool()
		return err
	case 'i':
		_, err := s.parseInt()
		return err
	case 'd':
		_, err := s.parseFloat()
		return err
	case 's', 'S':
		_, err := s.parseStringBytes()
		return err
	case 'a':
		if err := s.enter(); err != nil {
			return err
		}
		defer s.leave()
		n, err := s.parseArrayHeader()
		if err != nil {
			return err
		}
		for i := 0; i < n; i++ {
			if err := s.skipValue(); err != nil { // キー
				return err
			}
			if err := s.skipValue(); err != nil { // 値
				return err
			}
		}
		return s.expect('}')
	case 'O':
		if err := s.enter(); err != nil {
			return err
		}
		defer s.leave()
		_, n, err := s.parseObjectHeader()
		if err != nil {
			return err
		}
		for i := 0; i < n; i++ {
			if err := s.skipValue(); err != nil {
				return err
			}
			if err := s.skipValue(); err != nil {
				return err
			}
		}
		return s.expect('}')
	case 'r', 'R':
		// r:N; / R:N; — 参照はスカラー形式なので読み飛ばしは可能
		s.off++
		if err := s.expect(':'); err != nil {
			return err
		}
		if _, err := s.parseIntBody(); err != nil {
			return err
		}
		return s.expect(';')
	case 'E':
		// E:LEN:"Class:Case";
		s.off++
		if err := s.expect(':'); err != nil {
			return err
		}
		n, err := s.parseLen()
		if err != nil {
			return err
		}
		if err := s.expect(':'); err != nil {
			return err
		}
		if err := s.expect('"'); err != nil {
			return err
		}
		if n > len(s.data)-s.off {
			return s.syntaxErr(s.off, "enum length %d exceeds input", n)
		}
		s.off += n
		if err := s.expect('"'); err != nil {
			return err
		}
		return s.expect(';')
	case 'C':
		// C:LEN:"Class":PAYLOAD_LEN:{RAW}
		s.off++
		if err := s.expect(':'); err != nil {
			return err
		}
		nameLen, err := s.parseLen()
		if err != nil {
			return err
		}
		if err := s.expect(':'); err != nil {
			return err
		}
		if err := s.expect('"'); err != nil {
			return err
		}
		if nameLen > len(s.data)-s.off {
			return s.syntaxErr(s.off, "class name length %d exceeds input", nameLen)
		}
		s.off += nameLen
		if err := s.expect('"'); err != nil {
			return err
		}
		if err := s.expect(':'); err != nil {
			return err
		}
		payload, err := s.parseLen()
		if err != nil {
			return err
		}
		if err := s.expect(':'); err != nil {
			return err
		}
		if err := s.expect('{'); err != nil {
			return err
		}
		if payload > len(s.data)-s.off {
			return s.syntaxErr(s.off, "custom payload length %d exceeds input", payload)
		}
		s.off += payload
		return s.expect('}')
	}
	return s.syntaxErr(s.off, "unexpected token %q", tag)
}
