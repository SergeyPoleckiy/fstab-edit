package logger

import "testing"

func TestRedact(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"newline escaped", "a\nb", `a\nb`},
		{"carriage return escaped", "a\rb", `a\rb`},
		{"tab escaped", "a\tb", `a\tb`},
		{"backslash doubled", `a\b`, `a\\b`},
		{"url credentials", "https://user:pass@host/path", "https://user:***@host/path"},
		{"url credentials empty user", "https://:pass@host", "https://:***@host"},
		{"password option", "credentials=/secret/fstab.cred", "credentials=***"},
		{"password inline option", "rw,password=abc123,x", "rw,password=***,x"},
		{"token query", "url=https://h/?token=abc", "url=https://h/?token=***"},
		{"username option masked", "username=alice,password=x", "username=***,password=***"},
		{"ordinary text untouched", "UUID=abc / ext4 rw 0 1", "UUID=abc / ext4 rw 0 1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Redact(tc.in); got != tc.want {
				t.Fatalf("Redact(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
