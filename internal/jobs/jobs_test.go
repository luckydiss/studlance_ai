package jobs

import "testing"

func TestNormalizePath(t *testing.T) {
	cases := []struct {
		in      string
		want    string
		wantErr bool
	}{
		{"задание.pdf", "задание.pdf", false},
		{"папка/файл.txt", "папка/файл.txt", false},
		{`папка\файл.txt`, "папка/файл.txt", false},
		{"/abs.txt", "abs.txt", false},
		{"a//b.txt", "a/b.txt", false},
		{"../x", "", true},
		{"a/../../x", "", true},
		{"a/./b", "", true},
		{"", "", true},
	}
	for _, c := range cases {
		got, err := NormalizePath(c.in)
		if c.wantErr {
			if err == nil {
				t.Fatalf("%q: expected error, got %q", c.in, got)
			}
			continue
		}
		if err != nil {
			t.Fatalf("%q: %v", c.in, err)
		}
		if got != c.want {
			t.Fatalf("%q: got %q want %q", c.in, got, c.want)
		}
	}
}

func TestTitleFromPrompt(t *testing.T) {
	got := TitleFromPrompt("Курсовая работа вариант 14 всё по методичке кафедры и ещё слова сверху")
	if got != "Курсовая работа вариант 14 всё по методичке кафедры" {
		t.Fatalf("title %q", got)
	}
}
