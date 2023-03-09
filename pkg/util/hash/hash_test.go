package hash

import "testing"

func TestHashStringMap(t *testing.T) {
	var tests = []struct {
		m     map[string]string
		value string
	}{
		{
			m:     nil,
			value: "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
		},
		{
			m: map[string]string{
				"hello": "world",
				"foo":   "cat",
			},
			value: "feb9cffae8323043291d66857185fc26aa1a233e385b340a8b3cc49ca31530d5",
		},
	}

	for i, test := range tests {
		value := HashStringMap(test.m)
		if value != test.value {
			t.Fatalf("[%d]: expect %s, but got %s", i, test.value, value)
		}
	}
}
