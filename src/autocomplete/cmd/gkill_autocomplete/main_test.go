package main

import (
	"strings"
	"testing"
)

// resolveUserIDs は --user の値を整える。
//
// パスワードを受け取る口はもう無いので、ここが「誰として動くか」を
// 決める唯一の入口になる。空のまま素通りさせると、保存先の行が
// どの利用者のものか分からなくなる。
func TestResolveUserIDs(t *testing.T) {
	cases := []struct {
		name  string
		input []string
		want  []string
	}{
		{name: "1人", input: []string{"testuser"}, want: []string{"testuser"}},
		{name: "複数", input: []string{"testuser", "testuser_all"}, want: []string{"testuser", "testuser_all"}},
		// 同じ人を二度書いても App は1つでよい。
		{name: "重複は畳む", input: []string{"testuser", "testuser"}, want: []string{"testuser"}},
		{name: "前後の空白を落とす", input: []string{"  testuser  "}, want: []string{"testuser"}},
		{name: "空文字は無視する", input: []string{"testuser", "", "   "}, want: []string{"testuser"}},
		// 指定の順は保つ。解析の出力がその順に並ぶため。
		{name: "順序を保つ", input: []string{"b", "a"}, want: []string{"b", "a"}},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			userFlags = testCase.input
			t.Cleanup(func() { userFlags = nil })

			got, err := resolveUserIDs()
			if err != nil {
				t.Fatalf("エラーになった: %v", err)
			}
			if len(got) != len(testCase.want) {
				t.Fatalf("resolveUserIDs() = %v, want %v", got, testCase.want)
			}
			for i := range got {
				if got[i] != testCase.want[i] {
					t.Fatalf("resolveUserIDs() = %v, want %v", got, testCase.want)
				}
			}
		})
	}
}

// 利用者を指定しないまま動かすと、誰の記録を扱うのか決まらない。
// 黙って既定のアカウントを選ぶより、何を渡せばよいかを言って止まるほうがよい。
func TestResolveUserIDsRequiresAtLeastOne(t *testing.T) {
	for _, input := range [][]string{nil, {}, {""}, {"  "}} {
		userFlags = input
		t.Cleanup(func() { userFlags = nil })

		_, err := resolveUserIDs()
		if err == nil {
			t.Fatalf("利用者が空(%v)なのにエラーにならなかった", input)
		}
		// 直し方が書いてあること。エラー文だけ見て次の一手が分かるように。
		if !strings.Contains(err.Error(), "--user") {
			t.Errorf("直し方が書かれていない: %v", err)
		}
	}
}
