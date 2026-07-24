package phpserialize

import "testing"

// WordPress の _wp_attachment_metadata 相当の実データ形
var benchAttachment = []byte(`a:5:{s:5:"width";i:1200;s:6:"height";i:630;s:4:"file";s:19:"2024/01/example.jpg";s:8:"filesize";i:123456;s:5:"sizes";a:2:{s:9:"thumbnail";a:5:{s:4:"file";s:11:"example.jpg";s:5:"width";i:150;s:6:"height";i:150;s:9:"mime_type";s:10:"image/jpeg";s:8:"filesize";i:5000;}s:6:"medium";a:5:{s:4:"file";s:10:"medium.jpg";s:5:"width";i:300;s:6:"height";i:157;s:9:"mime_type";s:10:"image/jpeg";s:8:"filesize";i:9000;}}}`)

var benchList = []byte(`a:5:{i:0;s:6:"432845";i:1;s:5:"12345";i:2;s:4:"9876";i:3;s:3:"555";i:4;s:2:"42";}`)

func BenchmarkUnmarshalStruct(b *testing.B) {
	for b.Loop() {
		var m attachmentMeta
		if err := Unmarshal(benchAttachment, &m); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkUnmarshalAny(b *testing.B) {
	for b.Loop() {
		var v any
		if err := Unmarshal(benchAttachment, &v); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkUnmarshalStringSlice(b *testing.B) {
	for b.Loop() {
		var v []string
		if err := Unmarshal(benchList, &v); err != nil {
			b.Fatal(err)
		}
	}
}

// WithSparseArrayPadding 有効時の密データ (最頻パス) の回帰検知用。
func BenchmarkUnmarshalStringSliceSparse(b *testing.B) {
	dec := NewDecoder(WithSparseArrayPadding())
	for b.Loop() {
		var v []string
		if err := dec.Unmarshal(benchList, &v); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkMarshalStruct(b *testing.B) {
	size := 5000
	in := attachmentMeta{
		Width: 1200, Height: 630, File: "2024/01/example.jpg", Filesize: ptr(123456),
		Sizes: map[string]attachmentSize{
			"thumbnail": {File: "t.jpg", Width: 150, Height: 150, MimeType: "image/jpeg", Filesize: &size},
		},
	}
	for b.Loop() {
		if _, err := Marshal(in); err != nil {
			b.Fatal(err)
		}
	}
}
