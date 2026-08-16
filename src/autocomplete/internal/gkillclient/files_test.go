package gkillclient

// 視覚モデルへ送ってよい画像かの判定。
//
// **Content-Type は当てにならない。** gkill は拡張子で画像かを決めるので、
// RAW(.cr2)も .webp も image/* で返ってくる。しかし llama.cpp の復号器が
// 扱えるのは JPEG / PNG / GIF / BMP だけで、それ以外を送ると復号に失敗し、
// 「LLM がエラーを返した」という環境の問題に見える形で返ってくる。
// 2026-08-16 の実データでは .cr2 が 7,627件、.webp が 1,446件あり、
// 固まって並んでいるため5件連続失敗で解析が打ち切られた。

import "testing"

func TestIsDecodableImageAcceptsSupportedFormats(t *testing.T) {
	cases := map[string][]byte{
		"JPEG": {0xFF, 0xD8, 0xFF, 0xE0, 0x00, 0x10},
		"PNG":  {0x89, 'P', 'N', 'G', 0x0D, 0x0A},
		"GIF":  []byte("GIF89a...."),
		"BMP":  []byte("BM\x00\x00\x00\x00"),
	}
	for name, body := range cases {
		if !isDecodableImage(body) {
			t.Errorf("%s を送れない形式と判定した", name)
		}
	}
}

func TestIsDecodableImageRejectsUnsupportedFormats(t *testing.T) {
	cases := map[string][]byte{
		// Canon RAW。実データに 7,627件ある。TIFF風の先頭を持つ。
		"CR2": {'I', 'I', 0x2A, 0x00, 0x10, 0x00},
		// WebP は RIFF コンテナ。実データに 1,446件ある。
		"WebP": []byte("RIFF\x00\x00\x00\x00WEBPVP8 "),
		"PDF":  []byte("%PDF-1.7\n"),
		"ZIP":  {'P', 'K', 0x03, 0x04},
		"MP4":  []byte("\x00\x00\x00\x20ftypisom"),
		// HTML(エラーページが返ってきた場合)
		"HTML": []byte("<!DOCTYPE html>"),
		"空":    {},
	}
	for name, body := range cases {
		if isDecodableImage(body) {
			t.Errorf("%s を送れる形式と判定した（視覚モデルは復号できない）", name)
		}
	}
}
