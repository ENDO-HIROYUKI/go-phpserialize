package phpserialize

import (
	"bytes"
	"os/exec"
	"strings"
	"testing"
)

// TestDifferentialPHP は実 PHP との差分テスト。php コマンドが無い環境ではスキップする。
// 本ライブラリの Marshal 出力を PHP が unserialize でき、PHP の re-serialize を経ても
// 同じ値へデコードされることを確認する。
func TestDifferentialPHP(t *testing.T) {
	phpBin, err := exec.LookPath("php")
	if err != nil {
		t.Skip("php が見つからないためスキップ")
	}

	size := 5000
	values := []any{
		nil,
		true,
		false,
		int64(0),
		int64(-9223372036854775808),
		int64(9223372036854775807),
		0.1,
		1.0 / 3.0,
		1e15,
		-2.5e-8,
		"",
		"abc",
		"プロレス",
		"quote\" and \\ backslash \x00 nul",
		[]any{},
		[]any{int64(1), "a", true, nil, 0.5},
		map[string]any{"k": int64(1), "5": "v", "日本語": []any{int64(1)}},
		attachmentMeta{
			Width: 1200, Height: 630, File: "2024/01/エグザンプル.jpg",
			Filesize: ptr(123456),
			Sizes: map[string]attachmentSize{
				"thumbnail": {File: "t.jpg", Width: 150, Height: 150, MimeType: "image/jpeg", Filesize: &size},
			},
		},
	}

	const script = `
$d = stream_get_contents(STDIN);
$v = @unserialize($d);
if ($v === false && $d !== "b:0;") { fwrite(STDERR, "unserialize failed: " . $d); exit(1); }
echo serialize($v);
`

	for _, v := range values {
		ours, err := Marshal(v)
		if err != nil {
			t.Fatalf("Marshal(%#v): %v", v, err)
		}
		cmd := exec.Command(phpBin, "-r", strings.TrimSpace(script))
		cmd.Stdin = bytes.NewReader(ours)
		var stdout, stderr bytes.Buffer
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
		if err := cmd.Run(); err != nil {
			t.Errorf("PHP が本ライブラリの出力を unserialize できない: %v\n  input: %s\n  stderr: %s", err, ours, stderr.String())
			continue
		}
		var fromOurs, fromPHP any
		if err := Unmarshal(ours, &fromOurs); err != nil {
			t.Errorf("自身の出力をデコードできない: %v (%s)", err, ours)
			continue
		}
		if err := Unmarshal(stdout.Bytes(), &fromPHP); err != nil {
			t.Errorf("PHP re-serialize をデコードできない: %v (%s)", err, stdout.Bytes())
			continue
		}
		if !anyEqual(fromOurs, fromPHP) {
			t.Errorf("PHP 往復で値が変わった:\n  ours    %s → %#v\n  via php %s → %#v", ours, fromOurs, stdout.Bytes(), fromPHP)
		}
	}
}
