package phpserialize

import (
	"errors"
	"fmt"
	"reflect"
)

var (
	// ErrUnsupported は v1 で未対応のトークン (r: / R: / C: / E:) をデコード対象にしたときのエラー。
	ErrUnsupported = errors.New("phpserialize: unsupported token")
	// ErrDepth は入れ子が MaxDepth を超えたときのエラー。
	ErrDepth = errors.New("phpserialize: max depth exceeded")
	// ErrTrailingData は値の後に余分なデータが残っているときのエラー (WithAllowTrailingData で抑止可能)。
	ErrTrailingData = errors.New("phpserialize: trailing data after value")
	// ErrInvalidTarget は Unmarshal の第 2 引数が nil でないポインタ以外だったときのエラー。
	ErrInvalidTarget = errors.New("phpserialize: unmarshal target must be a non-nil pointer")
	// ErrSparsePaddingBudget は疎配列パディングの累積量がバジェットを超えたときのエラー。
	ErrSparsePaddingBudget = errors.New("phpserialize: sparse array padding budget exceeded")
)

// SyntaxError は入力が PHP シリアライズ形式として不正な場合のエラー。
type SyntaxError struct {
	Offset int // 入力先頭からのバイトオフセット
	Msg    string
}

func (e *SyntaxError) Error() string {
	return fmt.Sprintf("phpserialize: syntax error at offset %d: %s", e.Offset, e.Msg)
}

// TypeError は PHP 値を対象の Go 型へ変換できない場合のエラー。
type TypeError struct {
	Offset  int
	PHPType string
	GoType  reflect.Type
}

func (e *TypeError) Error() string {
	return fmt.Sprintf("phpserialize: cannot decode PHP %s into Go %s at offset %d", e.PHPType, e.GoType, e.Offset)
}

func phpTypeName(tag byte) string {
	switch tag {
	case 'N':
		return "null"
	case 'b':
		return "bool"
	case 'i':
		return "int"
	case 'd':
		return "float"
	case 's', 'S':
		return "string"
	case 'a':
		return "array"
	case 'O':
		return "object"
	case 'r', 'R':
		return "reference"
	case 'C':
		return "custom object"
	case 'E':
		return "enum"
	}
	return fmt.Sprintf("unknown token %q", tag)
}
